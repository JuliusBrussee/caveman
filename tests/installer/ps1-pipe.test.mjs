// Regression for #565: `irm .../install.ps1 | iex` crashed with
// "Cannot bind argument to parameter 'Path' because it is null."
//
// Two pipe-execution rules for install.ps1 (static checks — CI has no pwsh):
//   1. No top-level param() block. iex executes the file as a string, so a
//      top-level param can never receive arguments and (depending on host)
//      trips parsing. All logic lives in a function invoked at the bottom.
//   2. Script-path variables ($PSCommandPath / $MyInvocation.MyCommand.Path)
//      are $null under iex — any use must be guarded, never passed straight
//      into Split-Path (that was the #565 crash).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

function spawnAsync(cmd, args, opts = {}) {
  return new Promise((resolve) => {
    const child = spawn(cmd, args, {
      cwd: opts.cwd,
      env: opts.env,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    let timedOut = false;
    const timer = opts.timeout
      ? setTimeout(() => { timedOut = true; child.kill('SIGKILL'); }, opts.timeout)
      : null;
    child.stdout.on('data', (d) => { stdout += d; });
    child.stderr.on('data', (d) => { stderr += d; });
    child.on('error', (error) => {
      if (timer) clearTimeout(timer);
      resolve({ status: null, stdout, stderr, error });
    });
    child.on('close', (status) => {
      if (timer) clearTimeout(timer);
      resolve({ status, stdout, stderr, error: timedOut ? new Error('ETIMEDOUT') : undefined });
    });
    child.stdin.end(opts.input ?? '');
  });
}

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const PS1 = fs.readFileSync(path.join(REPO_ROOT, 'install.ps1'), 'utf8');

// Strip comment lines so doc mentions of param()/path vars don't false-positive.
const code = PS1.split('\n').filter(l => !/^\s*#/.test(l)).join('\n');

test('#565 install.ps1 has no top-level param block (everything inside a function)', () => {
  const beforeFunction = code.slice(0, code.indexOf('function '));
  assert.ok(code.includes('function '), 'install.ps1 must wrap its logic in a function for iex piping');
  assert.ok(
    !/param\s*\(/i.test(beforeFunction),
    'install.ps1 must not declare a top-level param() — it cannot receive args under `irm | iex` (issue #565)',
  );
});

test('#565 install.ps1 never uses $MyInvocation.MyCommand.Path (null under iex)', () => {
  assert.ok(
    !/\$MyInvocation\.MyCommand\.Path/i.test(code),
    'install.ps1 must not rely on $MyInvocation.MyCommand.Path — it is $null when piped to iex (issue #565)',
  );
});

test('#565 install.ps1 guards $PSCommandPath before Split-Path', () => {
  if (/\$PSCommandPath/i.test(code)) {
    assert.match(
      code,
      /if\s*\(\s*\$PSCommandPath\s*\)/i,
      '$PSCommandPath is $null under `irm | iex` — it must be truthiness-guarded before use (issue #565)',
    );
  }
});

test('#565 install.ps1 invokes its function at the bottom (script still does something)', () => {
  const lastLines = code.trim().split('\n').slice(-3).join('\n');
  assert.match(
    lastLines,
    /Install-Caveman/,
    'install.ps1 must actually invoke Install-Caveman after defining it',
  );
});

test('npm 12+ opts the root package into git fetching', () => {
  // stderr is discarded on both shims, so an npm notice cannot floor the major to 0.
  assert.match(code, /\$npxVersion\s*=\s*\[string\]\(& npx --version 2>\$null\)/i);
  assert.match(
    code,
    /if\s*\(\s*\$npxMajor\s+-ge\s+12\s*\)\s*\{[\s\S]*?& npx --allow-git=root -y/i,
    'npm 12+ must pass --allow-git=root before the GitHub package spec',
  );
  assert.match(
    code,
    /else\s*\{\s*& npx -y/i,
    'older npm versions must keep the legacy npx invocation',
  );
  // The .sh twin is covered in shim-npm-version.test.mjs: both shims must keep
  // the immutable release pin on BOTH npm branches.
  assert.equal(
    (code.match(/"github:\$Repo#\$PinnedRef"/g) || []).length,
    3,
    'the npm 12+, legacy npm, and Bun branches must pin the ref',
  );
});

// `irm | iex` runs the script inside the user's own session, so an `exit`
// anywhere in Install-Caveman closes their PowerShell window over the output
// it just printed. Only a run from a file may exit (with the result).
test('install.ps1 never exits the session that piped it to iex', () => {
  const body = code.slice(code.indexOf('function Install-Caveman'), code.lastIndexOf('Install-Caveman -InstallerArgs'));
  assert.doesNotMatch(body, /\bexit\b/i);
  assert.match(code, /if\s*\(\s*\$PSCommandPath\s*\)\s*\{\s*exit \$LASTEXITCODE\s*\}\s*$/);
});

// The same with a real pwsh where one exists: the session survives the pipe
// and still sees the result; a file run still exits with it. Stub node and
// npx (on a pseudo-terminal for the first run) stand in for the machine.
const pwsh = spawnSync('sh', ['-c', 'command -v pwsh'], { encoding: 'utf8' }).stdout?.trim();
const python = spawnSync('sh', ['-c', 'command -v python3'], { encoding: 'utf8' }).stdout?.trim();
test('install.ps1 under pwsh: iex keeps the session, the first run follows the rules', { skip: process.platform === 'win32' || !pwsh || !python }, async () => {
  const cli = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, 'packages', 'cli', 'package.json'), 'utf8')).version;
  const makeFixture = (cavemanVersion = null) => {
    const cwd = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-ps1-iex-'));
    const bin = path.join(cwd, 'bin');
    fs.mkdirSync(bin);
    // node answers the version questions, runs the PATH CLI probe and is the
    // local installer; npx is the remote one.
    fs.writeFileSync(path.join(bin, 'node'), `#!/bin/sh\n[ "$1" = -e ] && exec '${process.execPath}' "$@"\ncase "$2" in *split*) echo 24 ;; *versions*) echo 24.0.0 ;; *) exit $STUB_EXIT ;; esac\n`, { mode: 0o755 });
    fs.writeFileSync(path.join(bin, 'npx'), `#!/bin/sh\necho "$*" >> '${path.join(cwd, 'npx.log')}'\n[ "$1" = --version ] && echo 10.0.0\nexit $STUB_EXIT\n`, { mode: 0o755 });
    if (cavemanVersion !== null) {
      fs.writeFileSync(path.join(bin, 'caveman'), `#!/bin/sh\nprintf '{\\n  "version": "%s"\\n}\\n' ${cavemanVersion}\n`, { mode: 0o755 });
    }
    const env = (exit) => ({ HOME: cwd, PATH: `${bin}:/usr/bin:/bin`, TERM: 'xterm', STUB_EXIT: String(exit), POWERSHELL_TELEMETRY_OPTOUT: '1', POWERSHELL_UPDATECHECK: 'Off' });
    return { cwd, env, cleanup: () => fs.rmSync(cwd, { recursive: true, force: true }) };
  };
  const runPiped = async (exit, cavemanVersion = null, args = '') => {
    const f = makeFixture(cavemanVersion);
    try {
      return await spawnAsync(pwsh, ['-NoProfile', '-NonInteractive', '-Command',
        `Get-Content -Raw '${path.join(REPO_ROOT, 'install.ps1')}' | Invoke-Expression${args}; Write-Host "alive $LASTEXITCODE"`,
      ], { cwd: f.cwd, input: '', env: f.env(exit), timeout: 60_000 });
    } finally {
      f.cleanup();
    }
  };
  const runFile = async (exit) => {
    const f = makeFixture();
    try {
      return await spawnAsync(pwsh, ['-NoProfile', '-NonInteractive', '-File', path.join(REPO_ROOT, 'install.ps1')], {
        cwd: f.cwd, input: '', env: f.env(exit), timeout: 60_000,
      });
    } finally {
      f.cleanup();
    }
  };
  const runTerminal = async (...args) => {
    const f = makeFixture();
    try {
      const script = path.join(f.cwd, 'install.ps1');
      fs.copyFileSync(path.join(REPO_ROOT, 'install.ps1'), script);
      const out = await spawnAsync(python, ['-c', 'import pty,sys; sys.exit(pty.spawn(sys.argv[1:]) >> 8)', pwsh, '-NoProfile', '-File', script, ...args], {
        cwd: f.cwd, input: '', env: f.env(0), timeout: 60_000,
      });
      return { ...out, npx: fs.readFileSync(path.join(f.cwd, 'npx.log'), 'utf8') };
    } finally {
      f.cleanup();
    }
  };

  // The caveman on PATH runs the first run when it is this release or newer:
  // npx would put this release over a newer one.
  const [major, minor, patch] = cli.split('.').map(Number);
  const versionCases = [
    [cli, true],
    [`${major}.${minor}.${patch + 1}`, true],
    [`${major + 1}.0.0`, true],
    [`${cli}-rc.1`, false],
    ['0.0.1', false],
    ['banana', false],
  ];

  const runLimited = async (tasks, limit) => {
    const out = new Array(tasks.length);
    let next = 0;
    const worker = async () => {
      while (next < tasks.length) {
        const i = next++;
        out[i] = await tasks[i]();
      }
    };
    await Promise.all(Array.from({ length: Math.min(limit, tasks.length) }, worker));
    return out;
  };

  const limit = Math.max(1, Math.min(4, os.cpus().length - 1));
  const [ok, fail, file, interactive, quiet, ...versionResults] = await runLimited([
    () => runPiped(0),
    () => runPiped(3),
    () => runFile(3),
    () => runTerminal(),
    () => runTerminal('--non-interactive'),
    ...versionCases.map(([version, viaPath]) => async () => [version, viaPath, await runPiped(0, version)]),
  ], limit);

  assert.match(ok.stdout, new RegExp(`Next: npx -y @caveman-ai/cli@${cli.replaceAll('.', '\\.')} setup\nalive 0\n$`), ok.stdout + ok.stderr);
  assert.match(fail.stdout, /alive 3\n$/, 'a failed install leaves the session open and says so');
  assert.equal(file.status, 3, 'a file run still exits with the result');
  for (const [version, viaPath, res] of versionResults) {
    assert.match(res.stdout, viaPath ? /Next: caveman setup\n/ : /Next: npx -y @caveman-ai\/cli@/, version);
  }
  // In a terminal the first run starts, unless --non-interactive.
  assert.match(interactive.npx, /^-y @caveman-ai\/cli@\S+ setup$/m, 'a terminal starts the first run');
  assert.doesNotMatch(quiet.npx, /setup/);
  assert.match(quiet.stdout, /Next: npx -y @caveman-ai\/cli@\S+ setup/);
});
