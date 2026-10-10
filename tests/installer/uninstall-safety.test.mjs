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
import { createRequire } from 'node:module';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'installer', 'install.js');

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-uninstall-safety-'));
}

// Drop every PATH entry holding a `claude`/`gemini`/`caveman` binary so the
// installer never reaches the user's real plugin, extension or native-agent
// state.
function pathWithout(binNames) {
  const sep = process.platform === 'win32' ? ';' : ':';
  const exts = process.platform === 'win32' ? ['.exe', '.cmd', '.bat', ''] : [''];
  return (process.env.PATH || '')
    .split(sep)
    .filter(dir => {
      if (!dir) return false;
      for (const b of binNames) {
        for (const ext of exts) {
          try { if (fs.existsSync(path.join(dir, b + ext))) return false; } catch (_) {}
        }
      }
      return true;
    })
    .join(sep);
}

function fakeClaudeDir(root) {
  const dir = path.join(root, 'fake-bin');
  fs.mkdirSync(dir, { recursive: true });
  if (process.platform === 'win32') {
    fs.writeFileSync(path.join(dir, 'claude.cmd'), '@echo off\r\nexit /b 0\r\n');
  } else {
    const file = path.join(dir, 'claude');
    fs.writeFileSync(file, '#!/bin/sh\nexit 0\n');
    fs.chmodSync(file, 0o755);
  }
  return dir;
}

function isolatedEnv(root, extraBinDirs = []) {
  const home = path.join(root, 'home');
  const sep = process.platform === 'win32' ? ';' : ':';
  const bins = [fakeClaudeDir(root), ...extraBinDirs].join(sep);
  return {
    HOME: home,
    USERPROFILE: home,
    XDG_CONFIG_HOME: path.join(home, '.config'),
    HERMES_HOME: path.join(home, '.hermes'),
    OPENCLAW_WORKSPACE: path.join(home, '.openclaw', 'workspace'),
    PATH: `${bins}${sep}${pathWithout(['claude', 'gemini', 'caveman'])}`,
  };
}

// A fake `caveman` CLI that records each invocation (one argument per line,
// invocations separated by a blank line) into `record` and exits 0, the same
// shape `disableNativeAgent`'s real command uses for `caveman disable --all`.
// With `version` it answers `--version` the way the real CLI does.
function fakeCavemanDir(root, record, version) {
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
      + `if (process.argv[2] === '--version') console.log(${JSON.stringify(reply)});\n`);
    fs.writeFileSync(path.join(dir, 'caveman.cmd'),
      '@echo off\r\n'
      + '"%~dp0\\node.exe" "%~dp0\\caveman.js" %*\r\n');
  } else {
    const file = path.join(dir, 'caveman');
    fs.writeFileSync(file,
      '#!/bin/sh\n'
      + `{ for a in "$@"; do echo "$a"; done; echo; } >> "${record}"\n`
      + `[ "$1" = --version ] && echo '${reply}'\n`
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
const BUNDLED_CLI = (() => {
  try { return createRequire(INSTALLER).resolve('@caveman-ai/cli/package.json'); } catch (_) { return null; }
})();

test('uninstall runs the bundled CLI when the caveman on PATH is older', { skip: !BUNDLED_CLI && 'no node_modules/@caveman-ai/cli; run pnpm install' }, () => {
  const dir = freshTmpDir();
  const configDir = path.join(dir, 'claude');
  const record = path.join(dir, 'caveman-record.txt');
  try {
    for (const [version, runsBundled] of [['1.3.4', true], ['999.0.0', false]]) {
      const env = isolatedEnv(dir, [fakeCavemanDir(dir, record, version)]);
      const r = runInstaller(['--uninstall', '--dry-run'], configDir, env);
      assert.equal(r.status, 0, r.stderr || r.stdout);
      const line = r.stdout.split('\n').find((l) => l.includes('disable --all')) || '';
      if (runsBundled) {
        assert.ok(line.includes(path.dirname(BUNDLED_CLI)), `PATH caveman ${version} ran instead of the bundled CLI: ${line}`);
      } else {
        assert.match(line, /would run: caveman disable --all/, `bundled CLI ran over a newer PATH caveman: ${line}`);
      }
    }
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
    assert.equal(removed.status, 0, removed.stderr || removed.stdout);
    const output = `${removed.stdout}${removed.stderr}`;
    assert.match(output, /claude/);
    assert.match(output, /caveman disable --all/, 'name the command that withdraws the route');
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
    const mine = '{\n  // my note\n  "model": "opus",\n  "statusLine": {"type": "command", "command": "~/bin/s.sh",},\n}\n';
    fs.writeFileSync(settingsPath, mine);
    const r = runInstaller(['--uninstall'], configDir, env);
    assert.equal(r.status, 0, r.stderr || r.stdout);
    assert.equal(fs.readFileSync(settingsPath, 'utf8'), mine, 'uninstall rewrote a settings.json caveman never touched');
    assert.equal(fs.existsSync(`${settingsPath}.bak`), false);

    const hook = path.join(configDir, 'hooks', 'caveman-activate.js');
    const withHook = `{\n  // my note\n  "hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "node ${hook}"}]}]},\n}\n`;
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
