#!/usr/bin/env node
// upstream-check — should a new upstream release trigger integration?
//
// Usage: node scripts/upstream-check.js <name> <latestTag>
//   <name>       key in .github/upstream-versions.json (e.g. rtk)
//   <latestTag>  the latest release tag reported by the GitHub API (vX.Y.Z)
//
// Compares against the tracked version under that upstream's policy — the
// smallest semver component whose increase triggers integration:
//   major → only X bumps trigger
//   minor → X or Y bumps trigger
//   patch → any version increase triggers
//
// stdout: "trigger <version>" or "skip <reason>". Exit 0 either way; exit 1
// only on bad input. Pure stdlib, testable offline.

'use strict';

const fs = require('fs');
const path = require('path');

const STATE_PATH = path.resolve(__dirname, '..', '.github', 'upstream-versions.json');

function parseSemver(tag) {
  const m = /^v?(\d+)\.(\d+)\.(\d+)/.exec(String(tag || '').trim());
  if (!m) return null;
  return { major: +m[1], minor: +m[2], patch: +m[3] };
}

function decide(trackedTag, latestTag, policy) {
  const cur = parseSemver(trackedTag);
  const next = parseSemver(latestTag);
  if (!next) return { trigger: false, reason: `unparseable latest tag: ${latestTag}` };
  if (!cur) return { trigger: true, reason: 'no tracked version yet' };

  const newer =
    next.major > cur.major ||
    (next.major === cur.major && next.minor > cur.minor) ||
    (next.major === cur.major && next.minor === cur.minor && next.patch > cur.patch);
  if (!newer) return { trigger: false, reason: `latest ${latestTag} not newer than tracked ${trackedTag}` };

  const majorBumped = next.major > cur.major;
  const minorBumped = next.major === cur.major && next.minor > cur.minor;
  if (policy === 'major' && !majorBumped) {
    return { trigger: false, reason: `policy=major: ${trackedTag} → ${latestTag} is not a major bump` };
  }
  if (policy === 'minor' && !majorBumped && !minorBumped) {
    return { trigger: false, reason: `policy=minor: ${trackedTag} → ${latestTag} is only a patch bump` };
  }
  return { trigger: true, reason: `${trackedTag} → ${latestTag} meets policy=${policy}` };
}

function main() {
  const [name, latestTag] = process.argv.slice(2);
  if (!name || !latestTag) {
    process.stderr.write('usage: upstream-check.js <name> <latestTag>\n');
    process.exit(1);
  }
  const state = JSON.parse(fs.readFileSync(STATE_PATH, 'utf8'));
  const entry = state[name];
  if (!entry) {
    process.stderr.write(`upstream-check: unknown upstream '${name}' in ${STATE_PATH}\n`);
    process.exit(1);
  }
  const { trigger, reason } = decide(entry.version, latestTag, entry.policy);
  process.stdout.write(trigger ? `trigger ${latestTag}\n` : `skip ${reason}\n`);
  process.stderr.write(`upstream-check[${name}]: ${reason}\n`);
}

if (require.main === module) main();
module.exports = { parseSemver, decide };
