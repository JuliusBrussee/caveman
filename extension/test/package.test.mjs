import assert from "node:assert/strict";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import test from "node:test";

import { SHIPPABLE_FILES, verifyExtensionRoot } from "../scripts/verify-extension-stage.mjs";

const extensionRoot = resolve(import.meta.dirname, "..");

test("store package allowlist covers every runtime reference", () => {
  const result = verifyExtensionRoot(extensionRoot);
  assert.equal(result.manifest.manifest_version, 3);
  assert.equal(result.files.length, SHIPPABLE_FILES.length);
});

test("firefox manifest template is AMO-safe and version-injected at pack time", () => {
  const pkg = JSON.parse(readFileSync(join(extensionRoot, "package.json"), "utf8"));
  const chrome = JSON.parse(readFileSync(join(extensionRoot, "manifest.json"), "utf8"));
  // single version source: package.json == Chrome manifest
  assert.equal(pkg.version, chrome.version);
  const ff = JSON.parse(readFileSync(join(extensionRoot, "firefox/manifest.json"), "utf8"));
  // AMO ids are permanent and must claim no domain: UUID-style, exactly
  assert.match(
    ff.browser_specific_settings.gecko.id,
    /^\{[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\}$/,
  );
  // Firefox MV3 runs background event pages via `scripts`; never `service_worker`
  assert.deepEqual(ff.background?.scripts, ["src/background.js"]);
  assert.equal(ff.background?.service_worker, undefined);
  // no driftable hardcoded version in the template: the builder injects the shared one
  assert.equal(ff.version, undefined);
  const staged = { ...ff, version: chrome.version };
  assert.equal(staged.version, pkg.version);
  // AMO data-collection disclosure is mandatory for new extensions; this one collects nothing.
  // The key requires Firefox desktop 140+ / Firefox Android 142+, so strict_min_version
  // must be >= 142 to keep addons-linter zero-warning (KB: measured 2026-09-03).
  assert.deepEqual(ff.browser_specific_settings.gecko.data_collection_permissions, { required: ["none"] });
  const minParts = String(ff.browser_specific_settings.gecko.strict_min_version).split(".").map(Number);
  assert.ok(minParts[0] >= 140, "strict_min_version must be >= 140 for data_collection_permissions on Firefox desktop (no gecko_android target)");
});

test("stage verifier rejects files outside explicit allowlist", () => {
  const stage = mkdtempSync(join(tmpdir(), "caveman-extension-stage-"));
  try {
    for (const file of SHIPPABLE_FILES) {
      const target = join(stage, file);
      mkdirSync(dirname(target), { recursive: true });
      copyFileSync(join(extensionRoot, file), target);
    }
    verifyExtensionRoot(stage, { exact: true });
    writeFileSync(join(stage, "unreviewed.js"), "alert('unexpected')\n");
    assert.throws(
      () => verifyExtensionRoot(stage, { exact: true }),
      /staged allowlist mismatch; unexpected=unreviewed\.js/,
    );
  } finally {
    rmSync(stage, { recursive: true, force: true });
  }
});

test("stage verifier rejects relative url() in content-script css (KB #6441 guard)", () => {
  const stage = mkdtempSync(join(tmpdir(), "caveman-extension-stage-"));
  try {
    for (const file of SHIPPABLE_FILES) {
      const target = join(stage, file);
      mkdirSync(dirname(target), { recursive: true });
      copyFileSync(join(extensionRoot, file), target);
    }
    // the same reference in an extension-page css (popup.css) is legitimate and
    // must NOT trip the guard — it resolves against the extension origin
    const popupCss = join(stage, "popup.css");
    writeFileSync(popupCss, readFileSync(popupCss, "utf8") + "\n@font-face { src: url(fonts/geist-sans.woff2); }\n");
    verifyExtensionRoot(stage, { exact: true });
    // plant the exact D1 shape: a relative url() in the css the manifest
    // registers as a content_scripts stylesheet (injected into pages, so the
    // url() would resolve against the page origin and never load — KB #6441)
    const css = join(stage, "src/indicator.css");
    writeFileSync(css, readFileSync(css, "utf8") + "\n@font-face { src: url(../fonts/geist-mono.woff2); }\n");
    assert.throws(
      () => verifyExtensionRoot(stage, { exact: true }),
      /content-script stylesheet src\/indicator\.css references "\.\.\/fonts\/geist-mono\.woff2" with a relative url\(\)/,
    );
  } finally {
    rmSync(stage, { recursive: true, force: true });
  }
});
