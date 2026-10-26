import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import url, { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const HOOK = path.join(ROOT, 'src/hooks/caveman-activate.js');

test('SessionStart statusline command preserves a literal dollar-sign path', {
  skip: process.platform === 'win32' ? 'POSIX shell quoting behavior' : false,
}, (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-$CAVEMAN_PATH_SEGMENT-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const config = path.join(dir, 'claude');
  fs.mkdirSync(config);
  const env = {
    ...process.env,
    HOME: dir,
    USERPROFILE: dir,
    CLAUDE_CONFIG_DIR: config,
    CAVEMAN_DEFAULT_MODE: 'caveman',
  };
  delete env.CAVEMAN_PATH_SEGMENT;
  delete env.CLAUDE_CODE_ENTRYPOINT;

  const sessionId = '14000000-0000-4000-8000-000000000001';
  const activation = spawnSync(process.execPath, [HOOK], {
    cwd: dir,
    env,
    input: JSON.stringify({ source: 'startup', session_id: sessionId, cwd: dir }),
    encoding: 'utf8',
    timeout: 5000,
  });
  assert.equal(activation.status, 0, activation.stderr);
  const match = activation.stdout.match(/"statusLine":\s*(\{[^\n]+\})/);
  assert.ok(match, activation.stdout);
  const { command } = JSON.parse(match[1]);
  assert.ok(fs.existsSync(path.join(config, 'hooks/caveman-statusline.sh')));

  const statusline = spawnSync('/bin/sh', ['-c', command], {
    cwd: dir,
    env,
    input: JSON.stringify({ session_id: sessionId }),
    encoding: 'utf8',
    timeout: 5000,
  });
  assert(statusline.status === 0, statusline.stderr);
  assert.match(statusline.stdout, /\[CAVEMAN\]/);
});

// The hook is a plain script with no exports, so the helper is lifted out of
// its source. Static by necessity, and it keeps the three tests below honest
// about reading the shipped file rather than a copy.
function loadShellCommand() {
  const src = fs.readFileSync(path.join(ROOT, 'src/hooks/caveman-activate.js'), 'utf8');
  const extracted = src.match(/function shellCommand\(parts, platform = process\.platform\) \{[\s\S]*?\n\}/);
  assert.ok(extracted, 'shellCommand helper not found in caveman-activate.js');
  return new Function(`${extracted[0]}; return shellCommand;`)();
}

// The hook cannot require installer/lib/platform-paths.js — src/hooks/ ships
// without the installer tree beside it — so it carries its own copy of that
// quoting. Pin the two together: a nudge must suggest byte-for-byte what the
// installer would have registered, on BOTH platforms. Without this, a fix to
// one escaping convention silently leaves the other reachable (#1400 shipped
// the POSIX branch unescaped and the win32 branch stayed unescaped after it).
test('nudge shell quoting matches the installer hookCommand on both platforms', async () => {
  const shellCommand = loadShellCommand();

  const { hookCommand } = await import(
    url.pathToFileURL(path.join(ROOT, 'installer/lib/platform-paths.js')).href
  ).then((m) => m.default ?? m);

  const paths = [
    '/home/jane/.claude/hooks/caveman-statusline.sh',
    '/tmp/cave-$CAVEMAN_PATH_SEGMENT-x/claude/hooks/caveman-statusline.sh',
    '/home/Jane Doe/.claude/hooks/caveman-statusline.sh',
    '/tmp/back`tick/hooks/caveman-statusline.sh',
    '/tmp/qu"ote/hooks/caveman-statusline.sh',
    'C:\\Users\\RUNNER~1\\.claude\\hooks\\caveman-statusline.ps1',
  ];
  for (const platform of ['linux', 'win32']) {
    for (const p of paths) {
      assert.equal(
        shellCommand(['bash', p], platform),
        hookCommand('bash', [p], platform),
        `${platform} ${p}`,
      );
      assert.equal(
        shellCommand(['powershell', '-ExecutionPolicy', 'Bypass', '-File', p], platform),
        hookCommand('powershell', ['-ExecutionPolicy', 'Bypass', '-File', p], platform),
        `${platform} ps1 ${p}`,
      );
    }
  }
});

// Every metacharacter the shell would act on must be escaped in the suggested
// command. Pure string work, so it runs on every platform — this is the
// assertion that covers the win32 branch, which no POSIX runner can execute.
test('nudge shell quoting neutralizes shell metacharacters', () => {
  const shellCommand = loadShellCommand();
  const nasty = '/tmp/a$B`c"d/caveman-statusline.sh';
  assert.equal(
    shellCommand(['bash', nasty], 'linux'),
    '"bash" "/tmp/a\\$B\\`c\\"d/caveman-statusline.sh"',
  );
  // win32 adds `\` -> `/` on top of the same escaping.
  assert.equal(
    shellCommand(['powershell', '-File', 'C:\\Users\\a$B\\x.ps1'], 'win32'),
    '"powershell" "-File" "C:/Users/a\\$B/x.ps1"',
  );
});

// And the escaped form really does survive a shell. Needs a POSIX shell, so it
// is skipped on Windows exactly like the end-to-end test above; the escaping
// itself is asserted cross-platform by the test before this one.
test('a quoted metacharacter path survives a real POSIX shell', {
  skip: process.platform === 'win32' ? 'needs a POSIX shell' : false,
}, () => {
  const shellCommand = loadShellCommand();
  const nasty = '/tmp/a$B`c"d/caveman-statusline.sh';
  const quotedPath = shellCommand([nasty], 'linux');
  const out = spawnSync('/bin/sh', ['-c', `printf '%s' ${quotedPath}`], { encoding: 'utf8' });
  assert.equal(out.error, undefined, String(out.error));
  assert.equal(out.stdout, nasty);
});

// The drift test above pins the helper; this pins the CALL SITES. A fix that
// routes only the branch it was reported against through shellCommand leaves
// the sibling platform reachable, which is how #1400's win32 twin survived its
// first fix. Static, because `isWindows` is decided by process.platform and a
// POSIX runner can never execute the win32 branch end to end.
test('both statusline branches build their command through shellCommand', () => {
  const src = fs.readFileSync(path.join(ROOT, 'src/hooks/caveman-activate.js'), 'utf8');
  const block = src.match(/const command = isWindows[\s\S]*?;\n/);
  assert.ok(block, 'statusline command construction not found');
  const text = block[0];
  assert.equal(
    (text.match(/shellCommand\(/g) || []).length,
    2,
    `both branches must call shellCommand, got: ${text}`,
  );
  assert.ok(
    !/\$\{scriptPath\}/.test(text),
    `scriptPath must never be interpolated into a shell string raw: ${text}`,
  );
});
