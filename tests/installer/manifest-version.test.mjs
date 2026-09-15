// Every host that installs caveman shows the user a version, and reads it out
// of a different manifest. Those manifests were maintained by hand and drifted
// apart from the release (#107, #321):
//
//   .claude-plugin/plugin.json   no `version` key at all — `claude plugin list`
//                                falls back to the commit sha ("63e797cd753b"
//                                in #321) and `claude plugin update caveman`
//                                cannot resolve a version to compare against
//   gemini-extension.json        pinned at 1.0.1 — still what `gemini
//                                extensions list` reported to the reporter of
//                                #403 many releases later
//
// The release version itself already lives in two places that a test keeps
// honest: package.json `version`, and bin/install.js `PINNED_REF` (the
// immutable tag remote installs fetch from). These tests extend that guarantee
// to the manifests the hosts actually display, so a release bump can no longer
// leave a host reporting a version that has not shipped in a year.
//
// bin/install.js is deliberately not read from disk at runtime by the
// manifests — same reason as MIN_NODE_MAJOR in node-floor.test.mjs: the
// installer also runs detached from a checkout. The build is what holds them
// together.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');

const readJson = (rel) => JSON.parse(fs.readFileSync(path.join(REPO_ROOT, rel), 'utf8'));

const SEMVER = /^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/;

function releaseVersion() {
  const pkg = readJson('package.json');
  assert.ok(pkg.version, 'package.json declares no version');
  assert.match(pkg.version, SEMVER, `package.json version is not semver: ${pkg.version}`);
  return pkg.version;
}

// Manifests a host reads to tell the user which caveman they are running.
// plugins/caveman/.codex-plugin/plugin.json is the third one and is knowingly
// absent here: it is part of the mirrored plugin distribution, which this
// suite does not own. It is stale at 0.1.0 and tracked in #107.
const HOST_MANIFESTS = [
  '.claude-plugin/plugin.json',
  'gemini-extension.json',
];

for (const rel of HOST_MANIFESTS) {
  test(`${rel} declares the release version`, () => {
    const manifest = readJson(rel);
    assert.ok(
      manifest.version,
      `${rel} declares no version — the host falls back to a commit sha (#321)`
    );
    assert.equal(
      manifest.version,
      releaseVersion(),
      `${rel} version drifted from package.json`
    );
  });
}

// PINNED_REF is what a detached (curl|bash) install downloads from, and
// bin/lib/openclaw.js stamps the workspace skill with the same number. If the
// tag and the package version disagree, the manifests above are pinned to a
// release whose files are not the ones being installed.
test('bin/install.js PINNED_REF matches the release version', () => {
  const src = fs.readFileSync(path.join(REPO_ROOT, 'bin', 'install.js'), 'utf8');
  const m = /const PINNED_REF = process\.env\.CAVEMAN_REF \|\| '([^']+)';/.exec(src);
  assert.ok(m, 'PINNED_REF not found in bin/install.js');
  assert.equal(
    m[1].replace(/^v/, ''),
    releaseVersion(),
    `bin/install.js PINNED_REF (${m[1]}) drifted from package.json version`
  );
});
