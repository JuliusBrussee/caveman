# extension: the "Caveman Mode" browser extension

An MV3 extension for ChatGPT, Claude and Gemini. When it is on, it prepends the caveman directive
to each message you send and re-fires the send, so replies come back short and dense. It only
adds text to your outgoing message. There is no bundler: the browser loads the source files
directly. Savings are `inferred`.

## Layout
- `manifest.json`: MV3, `storage` permission only. Content scripts load `src/directive.js` before `src/caveman.js` (order matters) plus `src/indicator.css`. No web-accessible resources, no WASM.
- `firefox/manifest.json`: the Firefox manifest; the zip script stamps it with the main manifest's version.
- `src/directive.js`: the pure directive text (`buildPrimer`, `buildReminder`, `isPrefixed`, `normMode`). No chrome, DOM or network. It exposes `self.CavemanDirective` and the tests require it directly.
- `src/caveman.js`: the content script. A per-site composer adapter (`SITES` map) and capture-phase send interception. Sends the full primer on the first message and a short reminder after. `prependText` inserts at a collapsed selection without rebuilding the rich draft. A pending send cancels on draft, editor, conversation or settings changes; unknown controls keep their native behaviour.
- `src/background.js`: service worker that shows the on/off state on the toolbar badge.
- `popup.{html,js,css}`: on/off toggle, mode (`caveman`, `ultracave`, `megacave`), per-site switches, and a "Leave a review" link (Chrome Web Store via `chrome.runtime.id`; hidden on Firefox until a verified AMO URL exists).
- `test/`: Node tests for the directive and service worker; Playwright Chromium tests for the content script and provider composers (against `harness.html` and `provider-harness.html`) and for popup storage.
- `scripts/`: zip builder, stage verifier, Firefox e2e, store-asset renderer.

## Build and test (from the repo root)
- `npm --prefix extension ci`, then `npm --prefix extension test` (Node tests).
- `npm --prefix extension run test:browser` (Playwright Chromium; `npm --prefix extension run setup:browser` installs it).
- `npm --prefix extension run package` runs both suites, then writes `dist/caveman-browser-<version>.zip` and `dist/caveman-browser-firefox-<version>.zip`. CI's `extension` job runs this.
- The zip holds only the files in `SHIPPABLE_FILES` (`scripts/verify-extension-stage.mjs`), cross-checked against manifest, CSS and HTML references. A new runtime file must be added there.

## Invariants
- **Edits your outgoing message only.** Never reads model replies, never touches network requests, never automates anything beyond submitting the message you sent.
- **Local only.** No network requests, no analytics. Stores only `enabled`, `level` and `sites` in `chrome.storage.sync`.
- **Visible injection.** The directive shows plainly in the chat; the extension does not hide what it adds.
- **Inferred only.** Token savings come from the model's shorter replies; never label them `verified`.
- **Selectors are per site.** Composer and send selectors live in `SITES` in `src/caveman.js`. A site redesign means updating that entry; each keeps aria-label fallbacks so one rename does not break it.

See the root [AGENTS.md](../AGENTS.md).
