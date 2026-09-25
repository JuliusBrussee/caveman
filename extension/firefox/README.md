# Caveman Mode — Firefox (#810)

Firefox port of the existing Chrome MV3 extension. It runs the **same** runtime files
(`../src/directive.js`, `../src/caveman.js`, `../src/indicator.css`, `../popup.html`) so
behaviour is identical to Chrome; only the manifest is Firefox-tuned.
The popup hides the review link until a verified Firefox Add-ons listing URL is available.

Differences from the Chrome manifest:

- `browser_specific_settings.gecko.id` — a UUID-style id (`{2bcb73e7-…}`). AMO add-on ids
  are permanent, and a UUID-style id makes no domain claim. Must not change once
  published.
- `strict_min_version` 140.0 — the desktop floor required by
  `browser_specific_settings.gecko.data_collection_permissions` (mandatory AMO disclosure;
  Firefox desktop 140+, Firefox for Android would need 142+ and a `gecko_android` entry,
  which this manifest does not declare). All runtime APIs used (content scripts,
  storage, event-page background) are far older than 140.
- Icons omit the non-standard 32px key.
- Background uses the Firefox MV3 event-page form: `"background": { "scripts": ["src/background.js"] }`.
  Firefox does not run `background.service_worker`; the Chrome manifest keeps the
  service-worker key, the Firefox manifest uses `scripts`.
- **No `version` field.** The `firefox` pack target injects the version from the shared
  source (`package.json` == Chrome manifest, validated by
  `scripts/build-extension-zip.mjs`) at pack time, so the two manifests can never drift
  (see Pack for AMO below).

## Load (temporary, local test)

Load a **staged build**, not the raw template — the template intentionally carries no
`version` field (injected at pack time), and Firefox rejects a versionless manifest:

```bash
node extension/scripts/build-extension-zip.mjs firefox   # writes dist/stage/ + dist/*.zip
```

1. `about:debugging#/runtime/this-firefox` → **Load Temporary Add-on**.
2. Select `extension/dist/stage/manifest.json` (version injected, AMO-lint clean), or
   install `dist/caveman-browser-firefox-<ver>.zip`.

(Loading `extension/firefox/manifest.json` directly fails by design: no `version` field.
If you must load a folder, copy the shared referenced assets in and add a version locally —
but the staged build is the supported path.)

## Pack for AMO

The `firefox` target is wired into `extension/scripts/build-extension-zip.mjs`:

```bash
node extension/scripts/build-extension-zip.mjs firefox   # -> dist/caveman-browser-firefox-<ver>.zip
node extension/scripts/build-extension-zip.mjs            # -> dist/caveman-browser-<ver>.zip (Chrome, default)
```

The Firefox zip stages the same shared runtime files flat (`icons/`, `src/`, `popup.*`) with this
root-relative manifest (`manifest_version:3` + `browser_specific_settings.gecko`), injects the
shared version, and verifies the stage against the package allowlist (including the
`background.scripts` reference), so it uploads as `manifest_version:3` to addons.mozilla.org. No
store-asset or popup changes are the responsibility of this target.

The optional `npm run test:firefox` check needs Firefox and an already-installed
`web-ext` package. Set `FIREFOX_BIN` to the browser executable and `WEB_EXT_BIN` to
the package's JavaScript CLI when automatic discovery cannot find them. Paths may
contain spaces; the runner invokes Node directly without a shell. It never installs tools.

## Why this exists

https://github.com/JuliusBrussee/caveman/issues/810 — Firefox parity with the Chrome
extension so Firefox users also get caveman mode on ChatGPT, Claude and Gemini.---
