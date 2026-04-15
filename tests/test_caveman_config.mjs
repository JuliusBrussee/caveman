import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const canonicalConfigModule = require('../lib/caveman-config.js');
const hookConfigModule = require('../hooks/caveman-config.js');
const extensionConfigModule = require('../.github/extensions/caveman/config.js');
const canonicalConfigSource = readFileSync(new URL('../lib/caveman-config.js', import.meta.url), 'utf8');
const hookConfigSource = readFileSync(new URL('../hooks/caveman-config.js', import.meta.url), 'utf8');
const extensionConfigSource = readFileSync(new URL('../.github/extensions/caveman/config.js', import.meta.url), 'utf8');

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

function withEnv(patch, fn) {
  const original = {};
  for (const [key, value] of Object.entries(patch)) {
    original[key] = process.env[key];
    if (value === null) {
      delete process.env[key];
    } else {
      process.env[key] = value;
    }
  }

  try {
    fn();
  } finally {
    for (const [key, value] of Object.entries(original)) {
      if (value === undefined) {
        delete process.env[key];
      } else {
        process.env[key] = value;
      }
    }
  }
}

check('hook config file matches canonical', hookConfigSource === canonicalConfigSource);
check('extension config file matches canonical', extensionConfigSource === canonicalConfigSource);

for (const [label, module] of [
  ['canonical', canonicalConfigModule],
  ['hook', hookConfigModule],
  ['extension', extensionConfigModule],
]) {
  const tempRoot = mkdtempSync(join(tmpdir(), `caveman-config-${label}-`));

  try {
  const fakeHome = join(tempRoot, 'home');
  const xdgHome = join(tempRoot, 'xdg');
  const appData = join(tempRoot, 'appdata');
  const unixConfigDir = join(fakeHome, '.config', 'caveman');
  const xdgConfigDir = join(xdgHome, 'caveman');
  const appDataConfigDir = join(appData, 'caveman');

  mkdirSync(unixConfigDir, { recursive: true });
  mkdirSync(xdgConfigDir, { recursive: true });
  mkdirSync(appDataConfigDir, { recursive: true });

  withEnv({
    HOME: fakeHome,
    USERPROFILE: fakeHome,
    APPDATA: appData,
    XDG_CONFIG_HOME: null,
    CAVEMAN_DEFAULT_MODE: null,
  }, () => {
    writeFileSync(join(unixConfigDir, 'config.json'), '{"defaultMode":"wenyan-ultra"}', 'utf8');
    check(`${label}: reads ~/.config fallback`, module.getDefaultMode() === 'wenyan-ultra');
  });

  withEnv({
    HOME: fakeHome,
    USERPROFILE: fakeHome,
    APPDATA: appData,
    XDG_CONFIG_HOME: xdgHome,
    CAVEMAN_DEFAULT_MODE: null,
  }, () => {
    writeFileSync(join(xdgConfigDir, 'config.json'), '{"defaultMode":"lite"}', 'utf8');
    check(`${label}: XDG overrides ~/.config`, module.getDefaultMode() === 'lite');
  });

  withEnv({
    HOME: fakeHome,
    USERPROFILE: fakeHome,
    APPDATA: appData,
    XDG_CONFIG_HOME: null,
    CAVEMAN_DEFAULT_MODE: null,
  }, () => {
    rmSync(join(unixConfigDir, 'config.json'), { force: true });
    writeFileSync(join(appDataConfigDir, 'config.json'), '{"defaultMode":"ultra"}', 'utf8');
    check(`${label}: reads APPDATA fallback when ~/.config missing`, module.getDefaultMode() === 'ultra');
  });

  withEnv({
    HOME: fakeHome,
    USERPROFILE: fakeHome,
    APPDATA: appData,
    XDG_CONFIG_HOME: null,
    CAVEMAN_DEFAULT_MODE: 'full',
  }, () => {
    check(`${label}: env var overrides config files`, module.getDefaultMode() === 'full');
  });
  } finally {
    rmSync(tempRoot, { recursive: true, force: true });
  }
}

console.log('\n' + '='.repeat(50));
console.log(`Config tests: ${passed}/${passed + failed}` + (failed === 0 ? ' ✅ ALL PASSED' : ` ❌ ${failed} FAILURES`));
console.log('='.repeat(50));
process.exit(failed > 0 ? 1 : 0);
