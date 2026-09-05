// The Gemini extension install must not wait for input.
// See issue #400.
//
// A `curl | bash` install gives the child process a non-TTY stdin.
// Gemini CLI asks a workspace-trust question on that stdin.
// Gemini CLI also asks an extension-consent question on that stdin.
// The install then waits without an end.
//
// Gemini CLI v0.41.0 and later remove both prompts when the installer sends two parts together.
// The `--consent` flag answers the extension-install warning.
// The GEMINI_CLI_TRUST_WORKSPACE variable trusts the directory for this process.
// The trust prompt then does not run, and trustedFolders.json stays unchanged.
//
// The `--consent` flag alone also removes the trust prompt on some versions.
// But its yes-branch writes the current directory into trustedFolders.json.
// These tests hold the variable and the flag together.
//
// A Gemini CLI before v0.41.0 ignores the variable. The pair is not safe there.
// The installer therefore probes `gemini --help` one time.
// The text `--skip-trust` shows that this CLI reads the variable.
// The guarantee of no prompt holds only for that result.
// Without the flag, and after a failed probe, the installer runs the old command.
// The old command uses the caller directory, no variable, and no `--consent`.
// The CLI can then ask its own questions, as before this change.
//
// A trusted directory lets Gemini CLI load .gemini/ config and .env files.
// Gemini CLI also searches every parent directory for those files.
// The installer runs the modern command in a new empty scratch directory below ~/.caveman/tmp.
// Every parent then belongs to the user or to root, and the trust covers nothing.
// Gemini CLI keeps per-project state in ~/.gemini/tmp, so the scratch directory is not there.
// The directory comes from mkdtemp. The installer removes only that directory.
// If the installer cannot create that directory, it must not start Gemini CLI.
// These tests check the directory, the failure path, and existing user data too.

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
const URL = 'https://github.com/JuliusBrussee/caveman';
const IS_WIN = process.platform === 'win32';

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-gemini-'));
}

// Write a fake `gemini` into its own directory.
// The directory goes first in PATH, and the first match wins.
// Thus a real `gemini` on the machine needs no removal.
// The fake answers `extensions list` with no extensions and records nothing for it.
// The fake answers a bare `--help` with a short root help text and records nothing for it.
// The help text holds the `--skip-trust` line only when the option skipTrust is true.
// The options helpExit and helpStream give the exit status and the output stream of that answer.
// For other calls it records the trust variable, its cwd, and each argument in CAVEMAN_TEST_RECORD.
//
// On Windows the installer starts a `.cmd` file only when it is a Node shim.
// `portableInvocation` reads the shim, finds the script, and starts it with `process.execPath`.
// So the Windows fake is `gemini.js` plus a shim in the shape that `parseWindowsNodeShim` accepts.
const HELP_HEAD = [
  'gemini [options]',
  '',
  'Options:',
  '  -m, --model                 Model                          [string]',
];
const HELP_SKIP_TRUST = '  --skip-trust                Trust the current workspace for this session.  [boolean] [default: false]';
const HELP_TAIL = [
  '  -h, --help                  Show help                     [boolean]',
];

function helpLines(skipTrust) {
  return skipTrust ? [...HELP_HEAD, HELP_SKIP_TRUST, ...HELP_TAIL] : [...HELP_HEAD, ...HELP_TAIL];
}

function fakeGeminiDir(root, options = {}) {
  const { exitCode = 0, skipTrust = true, helpExit = 0, helpStream = 'stdout' } = options;
  const help = helpLines(skipTrust);
  const dir = path.join(root, 'fake-bin');
  fs.mkdirSync(dir, { recursive: true });
  if (IS_WIN) {
    fs.writeFileSync(path.join(dir, 'gemini.js'),
      "const fs = require('node:fs');\n"
      + 'const args = process.argv.slice(2);\n'
      + "if (args[0] === 'extensions' && args[1] === 'list') { console.log('No extensions installed.'); process.exit(0); }\n"
      + `if (args.length === 1 && args[0] === '--help') {\n`
      + `  process.${helpStream}.write(${JSON.stringify(help.join('\n') + '\n')});\n`
      + `  process.exit(${helpExit});\n`
      + '}\n'
      + 'const lines = [\n'
      + "  'GEMINI_CLI_TRUST_WORKSPACE=' + (process.env.GEMINI_CLI_TRUST_WORKSPACE || ''),\n"
      + "  'CWD=' + fs.realpathSync.native(process.cwd()),\n"
      + '  ...args,\n'
      + '];\n'
      + "fs.appendFileSync(process.env.CAVEMAN_TEST_RECORD, lines.join('\\n') + '\\n');\n"
      + `process.exit(${exitCode});\n`);
    fs.writeFileSync(path.join(dir, 'gemini.cmd'),
      '@echo off\r\n'
      + '"%~dp0\\node.exe" "%~dp0\\gemini.js" %*\r\n');
  } else {
    const file = path.join(dir, 'gemini');
    const redirect = helpStream === 'stderr' ? ' 1>&2' : '';
    fs.writeFileSync(file,
      '#!/bin/sh\n'
      + 'if [ "$1" = extensions ] && [ "$2" = list ]; then echo "No extensions installed."; exit 0; fi\n'
      + 'if [ $# -eq 1 ] && [ "$1" = --help ]; then\n'
      + `  cat <<'EOF_HELP'${redirect}\n`
      + `${help.join('\n')}\n`
      + 'EOF_HELP\n'
      + `  exit ${helpExit}\n`
      + 'fi\n'
      + '{\n'
      + '  echo "GEMINI_CLI_TRUST_WORKSPACE=${GEMINI_CLI_TRUST_WORKSPACE-}"\n'
      + '  echo "CWD=$(pwd -P)"\n'
      + '  for a in "$@"; do echo "$a"; done\n'
      + '} >> "$CAVEMAN_TEST_RECORD"\n'
      + `exit ${exitCode}\n`);
    fs.chmodSync(file, 0o755);
  }
  return dir;
}

// The child environment starts from a copy of the test environment without GEMINI_CLI_TRUST_WORKSPACE.
// The legacy path keeps an inherited variable.
// Without that removal, the test cannot tell an inherited value from an installer-added value.
// The option extraEnv adds variables to the child environment after that removal.
function runInstaller(root, args, fakeBin, extraEnv = {}) {
  const home = path.join(root, 'home');
  fs.mkdirSync(home, { recursive: true });
  const record = path.join(root, 'record.txt');
  const sep = IS_WIN ? ';' : ':';
  const baseEnv = { ...process.env };
  delete baseEnv.GEMINI_CLI_TRUST_WORKSPACE;
  const r = spawnSync(process.execPath, [
    INSTALLER, ...args,
    '--config-dir', path.join(root, 'claude'),
    '--no-mcp-shrink',
  ], {
    env: {
      ...baseEnv,
      HOME: home,
      USERPROFILE: home,
      XDG_CONFIG_HOME: path.join(home, '.config'),
      NO_COLOR: '1',
      CAVEMAN_TEST_RECORD: record,
      PATH: `${fakeBin}${sep}${process.env.PATH || ''}`,
      ...extraEnv,
    },
    input: '',
    encoding: 'utf8',
  });
  return { result: r, record, home };
}

// Split the record into tokens.
// The POSIX fake writes one token on each line.
// The Windows fake writes all arguments on one line.
// Therefore split on each space and on each line end.
function tokens(record) {
  return fs.readFileSync(record, 'utf8')
    .split(/\r?\n/)
    .filter((line) => !line.startsWith('CWD='))
    .join(' ')
    .split(/\s+/)
    .filter(Boolean);
}

// The path can contain spaces, so read the CWD line whole.
function recordedCwd(record) {
  const line = fs.readFileSync(record, 'utf8').split(/\r?\n/).find((l) => l.startsWith('CWD='));
  assert.ok(line, 'fake gemini did not record its cwd');
  return line.slice('CWD='.length).trim();
}

// The install must run in a new directory below <home>/.caveman/tmp.
// It must not run in the caller's directory or below the shared OS temp root.
// The installer must remove the directory after the run.
// Windows can give the temp root a short 8.3 name, so resolve both parents with the native realpath.
function assertScratchCwd(record, home) {
  const cwd = recordedCwd(record);
  const scratchParent = fs.realpathSync.native(path.join(home, '.caveman', 'tmp'));
  const parent = fs.realpathSync.native(path.dirname(cwd));
  assert.notEqual(path.resolve(cwd), path.resolve(process.cwd()), 'gemini ran in the caller cwd');
  assert.equal(parent, scratchParent, `gemini cwd is not below ~/.caveman/tmp: ${cwd}`);
  assert.ok(path.basename(cwd).startsWith('gemini-install-'), `unexpected scratch dir name: ${cwd}`);
  assert.equal(fs.existsSync(cwd), false, `scratch dir not removed: ${cwd}`);
}

// Check the recorded command.
// The option expectConsent selects the modern parts: the variable and `--consent`.
// The option expectScratch selects the scratch-directory check.
// The legacy path has no modern part, so both options are false there.
function assertInstallCall(record, home, options = {}) {
  const { expectConsent = true, expectScratch = expectConsent } = options;
  const t = tokens(record);
  const iExtensions = t.indexOf('extensions');
  const iInstall = t.indexOf('install');
  const iUrl = t.indexOf(URL);
  assert.ok(iExtensions >= 0 && iInstall > iExtensions, `bad subcommand: ${t.join(' ')}`);
  assert.ok(iUrl > iInstall, `repository URL missing or misplaced: ${t.join(' ')}`);
  if (expectConsent) {
    assert.ok(t.includes('GEMINI_CLI_TRUST_WORKSPACE=true'),
      `session-only trust variable not passed to gemini: ${t.join(' ')}`);
    assert.ok(t.indexOf('--consent') > iUrl, `--consent missing or before the URL: ${t.join(' ')}`);
  } else {
    assert.equal(t.includes('GEMINI_CLI_TRUST_WORKSPACE=true'), false,
      `legacy path passed the trust variable: ${t.join(' ')}`);
    assert.equal(t.includes('--consent'), false, `legacy path passed --consent: ${t.join(' ')}`);
  }
  if (expectScratch) assertScratchCwd(record, home);
}

function assertConsentCall(record, home) {
  assertInstallCall(record, home);
}

// The legacy path must run in the installer's own directory.
// It must also leave no scratch directory below <home>/.caveman/tmp.
function assertCallerCwd(record, home) {
  assert.equal(recordedCwd(record), fs.realpathSync.native(process.cwd()),
    'legacy path did not run in the caller cwd');
  const scratchParent = path.join(home, '.caveman', 'tmp');
  const left = fs.existsSync(scratchParent)
    ? fs.readdirSync(scratchParent).filter((name) => name.startsWith('gemini-install-'))
    : [];
  assert.deepEqual(left, [], `legacy path made a scratch dir: ${left.join(', ')}`);
}

// ── 1. Piped stdin with --non-interactive. The installer passes the two parts. ──
test('gemini install passes --consent and session-only trust (non-interactive)', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root);
    const { result, record, home } = runInstaller(root, ['--only', 'gemini', '--force', '--non-interactive'], fakeBin);
    assert.notEqual(result.status, 2, `argv error: ${result.stderr}`);
    assertConsentCall(record, home);
    assert.match(result.stdout, /gemini/);
    assert.match(result.stdout, /installed/i);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

// ── 2. The same call without --non-interactive. Stdin stays a pipe.
//      Thus the installer must not use a TTY branch. ──
test('gemini install needs no TTY branch: same command without --non-interactive', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root);
    const { result, record, home } = runInstaller(root, ['--only', 'gemini', '--force'], fakeBin);
    assert.notEqual(result.status, 2, `argv error: ${result.stderr}`);
    assertConsentCall(record, home);
    assert.match(result.stdout, /gemini/);
    assert.match(result.stdout, /installed/i);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

// ── 3. The dry run shows the command and the variable. It starts nothing. ──
test('gemini dry run prints the --consent command and the trust variable', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root);
    const { result, record } = runInstaller(root, ['--only', 'gemini', '--force', '--dry-run'], fakeBin);
    assert.notEqual(result.status, 2, `argv error: ${result.stderr}`);
    assert.ok(result.stdout.includes(`would run: gemini extensions install ${URL} --consent`),
      `dry-run line missing: ${result.stdout}`);
    assert.match(result.stdout, /GEMINI_CLI_TRUST_WORKSPACE=true/);
    assert.equal(fs.existsSync(record), false, 'dry run started gemini');
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

// ── 4. A CLI that supports session trust but fails. It rejects the command with exit code 1.
//      The installer must report a failure. It must not wait.
//      The record must show that the fake started with the full command.
//      Without that check, a launcher error gives the same message and the test passes falsely. ──
test('gemini install reports a failure when the CLI rejects the command', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root, { exitCode: 1 });
    const { result, record, home } = runInstaller(root, ['--only', 'gemini', '--force', '--non-interactive'], fakeBin);
    assert.equal(result.status, 1, `installer must exit 1 when its only agent fails: ${result.stdout}${result.stderr}`);
    const out = `${result.stdout}${result.stderr}`;
    assert.match(out, /gemini extensions install failed/);
    assert.ok(fs.existsSync(record), 'fake gemini did not start');
    assertConsentCall(record, home);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

// ── 5. The default path without --force. The installer first runs
//      `gemini extensions list`, then the install. This is the issue #400 path. ──
test('gemini install without --force runs the list preflight and then the same command', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root);
    const { result, record, home } = runInstaller(root, ['--only', 'gemini', '--non-interactive'], fakeBin);
    assert.notEqual(result.status, 2, `argv error: ${result.stderr}`);
    assertConsentCall(record, home);
    assert.match(result.stdout, /installed/i);
    assert.doesNotMatch(result.stdout, /already installed/);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

// ── 6. The scratch directory cannot be made. A file blocks the parent path.
//      The installer must report a failure and must not start Gemini CLI. ──
test('gemini install fails closed when the scratch directory cannot be created', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root);
    const home = path.join(root, 'home');
    fs.mkdirSync(path.join(home, '.caveman'), { recursive: true });
    fs.writeFileSync(path.join(home, '.caveman', 'tmp'), 'not a directory\n');
    const { result, record } = runInstaller(root, ['--only', 'gemini', '--force', '--non-interactive'], fakeBin);
    assert.equal(result.status, 1, `installer must exit 1 when its only agent fails: ${result.stdout}${result.stderr}`);
    const out = `${result.stdout}${result.stderr}`;
    assert.match(out, /could not create scratch directory/);
    assert.doesNotMatch(out, /\$ gemini extensions install/);
    assert.equal(fs.existsSync(record), false, 'gemini ran without a scratch directory');
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

// ── 7. User data below ~/.caveman/tmp must survive.
//      The installer removes only the directory that it created. ──
test('gemini install preserves existing files below ~/.caveman/tmp', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root);
    const home = path.join(root, 'home');
    const scratchParent = path.join(home, '.caveman', 'tmp');
    fs.mkdirSync(path.join(scratchParent, 'gemini-install'), { recursive: true });
    const sentinels = [
      path.join(scratchParent, 'user-data'),
      path.join(scratchParent, 'gemini-install', 'user-data'),
    ];
    for (const file of sentinels) fs.writeFileSync(file, 'keep me\n');
    const { result, record } = runInstaller(root, ['--only', 'gemini', '--force', '--non-interactive'], fakeBin);
    assert.notEqual(result.status, 2, `argv error: ${result.stderr}`);
    assertConsentCall(record, home);
    for (const file of sentinels) {
      assert.equal(fs.readFileSync(file, 'utf8'), 'keep me\n', `installer erased ${file}`);
    }
    const left = fs.readdirSync(scratchParent).filter((name) => name.startsWith('gemini-install-'));
    assert.deepEqual(left, [], `scratch dirs left behind: ${left.join(', ')}`);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

// ── 8. An older Gemini CLI with no `--skip-trust` in its help.
//      The installer must run the command from before this change.
//      That command uses the caller directory, no trust variable, and no `--consent`.
//      A `--consent` on such a CLI trusts the caller directory permanently. ──
test('gemini install falls back to the plain command on a CLI without --skip-trust', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root, { skipTrust: false });
    const { result, record, home } = runInstaller(root, ['--only', 'gemini', '--force', '--non-interactive'], fakeBin);
    assert.notEqual(result.status, 1, `installer must not fail: ${result.stdout}${result.stderr}`);
    assert.match(result.stdout, /gemini/);
    assert.match(result.stdout, /installed/i);
    assertInstallCall(record, home, { expectConsent: false });
    assertCallerCwd(record, home);
    assert.match(result.stdout, /no --skip-trust/);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

// ── 9. The dry run on an older CLI shows the plain command.
//      It shows no `--consent` and no trust variable. It starts nothing. ──
test('gemini dry run prints the plain command on a CLI without --skip-trust', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root, { skipTrust: false });
    const { result, record } = runInstaller(root, ['--only', 'gemini', '--force', '--dry-run'], fakeBin);
    assert.notEqual(result.status, 2, `argv error: ${result.stderr}`);
    const line = result.stdout.split(/\r?\n/).find((l) => l.includes('would run: gemini extensions install'));
    assert.ok(line, `dry-run line missing: ${result.stdout}`);
    assert.equal(line.trim(), `would run: gemini extensions install ${URL}`);
    assert.doesNotMatch(result.stdout, /GEMINI_CLI_TRUST_WORKSPACE=true/);
    assert.equal(fs.existsSync(record), false, 'dry run started gemini');
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

// ── 10. The probe fails with exit code 1, but its text holds `--skip-trust`.
//       A failed probe does not prove support, so the installer must use the plain command.
//       This checks the exit-status condition of the probe. ──
test('gemini install falls back to the plain command when the help probe fails', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root, { helpExit: 1 });
    const { result, record, home } = runInstaller(root, ['--only', 'gemini', '--force', '--non-interactive'], fakeBin);
    assert.notEqual(result.status, 1, `installer must not fail: ${result.stdout}${result.stderr}`);
    assertInstallCall(record, home, { expectConsent: false });
    assertCallerCwd(record, home);
    assert.match(result.stdout, /could not read/);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

// ── 11. The help text comes on standard error. The exit status is 0.
//       The probe must read both streams, so the modern command must run. ──
test('gemini install reads the help probe from stderr too', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root, { helpStream: 'stderr' });
    const { result, record, home } = runInstaller(root, ['--only', 'gemini', '--force', '--non-interactive'], fakeBin);
    assert.notEqual(result.status, 2, `argv error: ${result.stderr}`);
    assertConsentCall(record, home);
    assert.match(result.stdout, /installed/i);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

// ── 12. The user sets GEMINI_CLI_TRUST_WORKSPACE before the install, on a CLI without `--skip-trust`.
//       The legacy path keeps the inherited environment. It does not add and does not remove the variable.
//       The command still has no `--consent`, and it still runs in the caller directory. ──
test('gemini legacy path keeps an inherited GEMINI_CLI_TRUST_WORKSPACE', () => {
  const root = freshTmpDir();
  try {
    const fakeBin = fakeGeminiDir(root, { skipTrust: false });
    const { result, record, home } = runInstaller(root, ['--only', 'gemini', '--force', '--non-interactive'], fakeBin,
      { GEMINI_CLI_TRUST_WORKSPACE: 'true' });
    assert.notEqual(result.status, 1, `installer must not fail: ${result.stdout}${result.stderr}`);
    const t = tokens(record);
    assert.ok(t.includes('GEMINI_CLI_TRUST_WORKSPACE=true'), `inherited variable was removed: ${t.join(' ')}`);
    assert.equal(t.includes('--consent'), false, `legacy path passed --consent: ${t.join(' ')}`);
    assert.ok(t.indexOf(URL) > t.indexOf('install'), `repository URL missing or misplaced: ${t.join(' ')}`);
    assertCallerCwd(record, home);
    assert.match(result.stdout, /no --skip-trust/);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
