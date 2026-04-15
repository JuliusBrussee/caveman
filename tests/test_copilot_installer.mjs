import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { tmpdir } from 'node:os';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

let passed = 0;
let failed = 0;

function check(name, result) {
  if (result) {
    passed++;
  } else {
    failed++;
    console.log('FAIL: ' + name);
  }
}

const root = dirname(fileURLToPath(new URL('../', import.meta.url)));
const installer = fileURLToPath(new URL('../scripts/install-copilot-extension.mjs', import.meta.url));
const sourceExt = fileURLToPath(new URL('../.github/extensions/caveman/extension.mjs', import.meta.url));

const tempRoot = mkdtempSync(join(tmpdir(), 'caveman-copilot-install-'));

try {
  const targetRepo = join(tempRoot, 'target-repo');
  const tempHome = join(tempRoot, 'home');
  mkdirSync(join(targetRepo, '.git'), { recursive: true });
  mkdirSync(tempHome, { recursive: true });

  const firstInstall = spawnSync('node', [installer, targetRepo], {
    cwd: root,
    encoding: 'utf8',
  });
  check('installer exits 0 on first install', firstInstall.status === 0);
  check('installer prints success', (firstInstall.stdout || '').includes('Installed Caveman Copilot CLI extension'));

  const installedExt = join(targetRepo, '.github', 'extensions', 'caveman', 'extension.mjs');
  check('extension.mjs copied', existsSync(installedExt));
  check('parser.mjs copied', existsSync(join(targetRepo, '.github', 'extensions', 'caveman', 'parser.mjs')));
  check('rules.mjs copied', existsSync(join(targetRepo, '.github', 'extensions', 'caveman', 'rules.mjs')));
  check('copied extension matches source', readFileSync(installedExt, 'utf8') === readFileSync(sourceExt, 'utf8'));

  const secondInstall = spawnSync('node', [installer, targetRepo], {
    cwd: root,
    encoding: 'utf8',
  });
  check('installer refuses overwrite without force', secondInstall.status === 1);
  check('installer explains force flag', (secondInstall.stderr || '').includes('--force'));

  writeFileSync(installedExt, '// overwritten during test\n', 'utf8');
  const forcedInstall = spawnSync('node', [installer, targetRepo, '--force'], {
    cwd: root,
    encoding: 'utf8',
  });
  check('installer exits 0 with force', forcedInstall.status === 0);
  check('force restores extension contents', readFileSync(installedExt, 'utf8') === readFileSync(sourceExt, 'utf8'));

  const globalEnv = {
    ...process.env,
    HOME: tempHome,
    USERPROFILE: tempHome,
  };
  const globalInstall = spawnSync('node', [installer, '--global'], {
    cwd: root,
    encoding: 'utf8',
    env: globalEnv,
  });
  check('global installer exits 0', globalInstall.status === 0);
  check('global installer prints success', (globalInstall.stdout || '').includes('globally into'));

  const globalExt = join(tempHome, '.copilot', 'extensions', 'caveman', 'extension.mjs');
  check('global extension.mjs copied', existsSync(globalExt));
  check('global copied extension matches source', readFileSync(globalExt, 'utf8') === readFileSync(sourceExt, 'utf8'));

  const secondGlobalInstall = spawnSync('node', [installer, '--global'], {
    cwd: root,
    encoding: 'utf8',
    env: globalEnv,
  });
  check('global installer refuses overwrite without force', secondGlobalInstall.status === 1);
  check('global installer explains force flag', (secondGlobalInstall.stderr || '').includes('--force'));

  writeFileSync(globalExt, '// overwritten global install during test\n', 'utf8');
  const forcedGlobalInstall = spawnSync('node', [installer, '--global', '--force'], {
    cwd: root,
    encoding: 'utf8',
    env: globalEnv,
  });
  check('global installer exits 0 with force', forcedGlobalInstall.status === 0);
  check('global force restores extension contents', readFileSync(globalExt, 'utf8') === readFileSync(sourceExt, 'utf8'));
} finally {
  rmSync(tempRoot, { recursive: true, force: true });
}

console.log('\n' + '='.repeat(50));
console.log(`Installer tests: ${passed}/${passed + failed}` + (failed === 0 ? ' ✅ ALL PASSED' : ` ❌ ${failed} FAILURES`));
console.log('='.repeat(50));
process.exit(failed > 0 ? 1 : 0);
