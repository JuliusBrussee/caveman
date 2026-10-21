import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const source = fs.readFileSync(path.join(root, 'install.ps1'), 'utf8');
const shells = ['pwsh', ...(process.platform === 'win32' ? ['powershell.exe'] : [])]
  .filter(command => spawnSync(command, ['-NoProfile', '-NonInteractive', '-Command', '$PSVersionTable.PSVersion.ToString()'], { encoding: 'utf8' }).status === 0);
const quote = value => `'${String(value).replaceAll("'", "''")}'`;
const ref = 'v0.0.0-bun-test';

// Exercise both PowerShell editions where available; mocks test shim control
// flow and argument construction, not native Windows executable marshalling.
for (const shell of shells.length ? shells : [undefined]) {
function run(t, { node = false, bun = true, npx = true, major = 24, local = false, status = 0, args = ['--dry-run', '--config-dir', 'a path; & chars'] } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman bun ps '));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const script = path.join(dir, 'install.ps1');
  const log = path.join(dir, 'calls.jsonl');
  fs.writeFileSync(script, source);
  if (local) {
    fs.mkdirSync(path.join(dir, 'installer'));
    fs.writeFileSync(path.join(dir, 'installer/install.js'), '// fixture only');
  }
  // Mock only native commands, inside a real PowerShell process. Calls and
  // LASTEXITCODE are observable, and neither packages nor user homes are touched.
  const harness = `
$env:CAVEMAN_REF = ${quote(ref)}
function Get-Command {
  [CmdletBinding()] param([string]$Name)
  if (@{node=$${node};bun=$${bun};npx=$${npx}}[$Name]) { return @{Name=$Name} }
  return $null
}
function Save-Call($Name, $Values) {
  @{name=$Name;args=@($Values)} | ConvertTo-Json -Compress | Add-Content -Encoding UTF8 ${quote(log)}
  $global:LASTEXITCODE = ${status}
}
function node {
  if ($args[0] -eq '-p') {
    if ($args[1] -like '*split*') { ${major} } else { '${major}.0.0' }
    $global:LASTEXITCODE=0; return
  }
  Save-Call 'node' $args
}
function bun { Save-Call 'bun' $args }
function npx {
  if ($args[0] -eq '--version') { '12.0.0'; $global:LASTEXITCODE=0; return }
  Save-Call 'npx' $args
}
$InstallArgs = @(${args.map(quote).join(',')})
${local ? `& ${quote(script)} @InstallArgs` : `& ([scriptblock]::Create([IO.File]::ReadAllText(${quote(script)}))) @InstallArgs\nWrite-Host 'session-alive'`}
exit $LASTEXITCODE
`;
  const r = spawnSync(shell, ['-NoProfile', '-NonInteractive', '-Command', harness], {
    cwd: dir, encoding: 'utf8', timeout: 30000,
    env: { ...process.env, POWERSHELL_TELEMETRY_OPTOUT: '1', POWERSHELL_UPDATECHECK: 'Off' },
  });
  const calls = fs.existsSync(log) ? fs.readFileSync(log, 'utf8').replace(/^\uFEFF/, '').trim().split(/\r?\n/).map(line => JSON.parse(line.replace(/^\uFEFF/, ''))) : [];
  return { ...r, calls, dir };
}
for (const local of [false, true]) {
  test(`${shell || 'PowerShell unavailable'}: Bun fallback preserves args and pin (${local ? 'file' : 'pipe'})`, { skip: !shell }, t => {
    const r = run(t, { local });
    assert.equal(r.status, 0, r.stdout + r.stderr);
    assert.deepEqual(r.calls, [{name:'bun', args:[
      ...(local ? [path.join(r.dir, 'installer/install.js')] : ['x', '--bun', `github:JuliusBrussee/caveman#${ref}`]),
      '--dry-run', '--config-dir', 'a path; & chars',
    ]}]);
    if (!local) assert.match(r.stdout, /session-alive/);
  });
  test(`${shell || 'PowerShell unavailable'}: Bun failure propagates (${local ? 'file' : 'pipe'})`, { skip: !shell }, t => {
    const r = run(t, { local, status: 7, args: [] });
    assert.equal(r.status, 7, r.stdout + r.stderr);
    assert.doesNotMatch(r.stdout, /installer finished/);
    if (!local) assert.match(r.stdout, /session-alive/);
  });
}
test(`${shell || 'PowerShell unavailable'}: keeps Node first and npm 12 flags`, { skip: !shell }, t => {
  const r = run(t, { node: true });
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.equal(r.calls[0].name, 'npx');
  assert.deepEqual(r.calls[0].args.slice(0, 3), ['--allow-git=root', '-y', `github:JuliusBrussee/caveman#${ref}`]);
});
test(`${shell || 'PowerShell unavailable'}: rejects old Node or missing npx rather than using Bun`, { skip: !shell }, t => {
  for (const options of [{ node: true, major: 16 }, { node: true, npx: false }]) {
    const r = run(t, options);
    assert.notEqual(r.status, 0, r.stdout + r.stderr);
    assert.deepEqual(r.calls, []);
  }
});
test(`${shell || 'PowerShell unavailable'}: requires a runtime and stops before launching packages`, { skip: !shell }, t => {
  const r = run(t, { bun: false });
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /or Bun required/);
  assert.deepEqual(r.calls, []);
});
test(`${shell || 'PowerShell unavailable'}: Bun first run leaves the session open and names the Node requirement`, { skip: !shell }, t => {
  const r = run(t, { args: ['--non-interactive'] });
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.equal(r.calls.length, 1);
  assert.match(r.stdout, /needs Node 22\.13\+/);
  assert.match(r.stdout, /session-alive/);
});
}
