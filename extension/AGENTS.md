# extension — "Caveman Mode" browser extension (MV3, no bundler)

The consumer top-of-funnel: a toggle that, on ChatGPT / Claude / Gemini, prepends the **caveman
skill directive** to each message you send and re-fires the send, so the AI replies in a compact,
high-density style intended to preserve technical substance. It only ever adds text to your *outgoing*
message; it never reads replies, never touches network requests. Savings are `inferred`. (This is
the directive-injector product; an earlier revision shipped a local WASM compress button — EXT
removed it after Chrome Web Store review couldn't reproduce "compress prompt locally".)

## Layout
- `manifest.json` — MV3. `storage` permission only. Content scripts load `src/directive.js` **then** `src/caveman.js` (order matters). Only the fonts are web-accessible — no WASM, no special CSP.
- `src/directive.js` — the **pure** caveman directive text: `buildPrimer`/`buildReminder`/`isPrefixed`/`normLevel`. No chrome/DOM/network. Loaded first (exposes `self.CavemanDirective`) and required directly by the test suite.
- `src/caveman.js` — content script: per-site composer adapter + capture-phase send interception. Prepends the directive (full primer on the first message, short reminder after) and re-fires send. Resilient selectors; safe `setText` (never `textContent=`, which desyncs ProseMirror/Quill); polls for the enabled send button before clicking.
- `src/background.js` — service worker: just reflects the on/off state on the toolbar badge.
- `popup.{html,js,css}` — the on/off toggle, intensity (lite/full/ultra), per-site switches, and a **Leave-a-review** CTA (links to this install's Chrome Web Store reviews via `chrome.runtime.id`).
- `test/` — pure directive/service-worker tests plus real Chromium
  content-script and popup/storage journeys against `harness.html`.

## Conventions & honesty
- **Edits your outgoing message only.** Prepends a visible directive; never reads model replies, never touches network requests, never automates anything beyond submitting the message you sent.
- **Local-only.** No network requests, no analytics. Stores only `enabled` / `level` / `sites` in `chrome.storage.sync`.
- **Honest about injection.** The directive is plainly visible in the chat — the extension doesn't hide what it adds.
- **inferred-only.** Token savings are an effect of the model's terser replies; never labeled `verified`.

## Conventions
- Build/test: `npm test` for pure tests; `npm run test:browser` for Chromium send interception, popup storage,
  per-site controls, and review-link behavior. There is no
  build step — MV3 loads the source directly.
- Package: `npm run package` runs both suites, then writes `dist/caveman-browser-<version>.zip`. Staged set uses explicit allowlist in `scripts/verify-extension-stage.mjs`, cross-checked against manifest/CSS/HTML references.
- Composer + send selectors live in the `SITES` map in `src/caveman.js`; a site redesign means updating it (each entry keeps aria-label fallbacks so one rename doesn't break it).

See ../../CLAUDE.md (root)
