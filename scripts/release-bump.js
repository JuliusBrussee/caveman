#!/usr/bin/env node
// release-bump — the single place that knows every version sync point.
//
// Sync points (all must agree or releases drift):
//   pyproject.toml                      version = "X.Y.Z"
//   python/caveman_agent/__init__.py    __version__ = "X.Y.Z"
//   bin/install.js                      PINNED_REF fallback 'vX.Y.Z'
//   package.json                        "version": "X.Y.Z"
//
// Usage:
//   node scripts/release-bump.js --check                 # verify all in sync, print version
//   node scripts/release-bump.js --bump patch|minor|major
//   node scripts/release-bump.js --set 2.0.0
//
// Prints the resulting version on stdout (the workflow captures it). Exits 1
// on inconsistency or bad input. Pure stdlib.

'use strict';

const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..');

const SYNC_POINTS = [
  {
    file: 'pyproject.toml',
    re: /^version = "(\d+\.\d+\.\d+)"$/m,
    render: v => `version = "${v}"`,
  },
  {
    file: 'python/caveman_agent/__init__.py',
    re: /^__version__ = "(\d+\.\d+\.\d+)"$/m,
    render: v => `__version__ = "${v}"`,
  },
  {
    file: 'bin/install.js',
    re: /^const PINNED_REF = process\.env\.CAVEMAN_REF \|\| 'v(\d+\.\d+\.\d+)';$/m,
    render: v => `const PINNED_REF = process.env.CAVEMAN_REF || 'v${v}';`,
  },
  {
    file: 'package.json',
    re: /^  "version": "(\d+\.\d+\.\d+)",$/m,
    render: v => `  "version": "${v}",`,
  },
];

function readVersions() {
  return SYNC_POINTS.map(sp => {
    const p = path.join(ROOT, sp.file);
    const body = fs.readFileSync(p, 'utf8');
    const m = sp.re.exec(body);
    if (!m) throw new Error(`${sp.file}: version pattern not found`);
    return { ...sp, path: p, body, version: m[1] };
  });
}

function bump(version, kind) {
  const [maj, min, pat] = version.split('.').map(Number);
  if (kind === 'major') return `${maj + 1}.0.0`;
  if (kind === 'minor') return `${maj}.${min + 1}.0`;
  if (kind === 'patch') return `${maj}.${min}.${pat + 1}`;
  throw new Error(`unknown bump kind: ${kind} (patch|minor|major)`);
}

function main() {
  const args = process.argv.slice(2);
  const points = readVersions();
  const versions = new Set(points.map(p => p.version));
  if (versions.size !== 1) {
    for (const p of points) process.stderr.write(`  ${p.file}: ${p.version}\n`);
    process.stderr.write('release-bump: version sync points DISAGREE — fix before releasing\n');
    process.exit(1);
  }
  const current = points[0].version;

  if (args[0] === '--check' || args.length === 0) {
    process.stdout.write(current + '\n');
    return;
  }

  let next;
  if (args[0] === '--bump') next = bump(current, args[1]);
  else if (args[0] === '--set') {
    next = args[1];
    if (!/^\d+\.\d+\.\d+$/.test(next || '')) {
      process.stderr.write(`release-bump: --set needs X.Y.Z, got: ${next}\n`);
      process.exit(1);
    }
  } else {
    process.stderr.write('usage: release-bump.js [--check | --bump patch|minor|major | --set X.Y.Z]\n');
    process.exit(1);
  }

  for (const p of points) {
    fs.writeFileSync(p.path, p.body.replace(p.re, p.render(next)));
  }
  process.stdout.write(next + '\n');
}

main();
