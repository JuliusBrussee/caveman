# Caveman Mode (browser extension)

Toggle **caveman mode** on ChatGPT, Claude, and Gemini. When it's on, the
extension adds a short caveman instruction to each message you send, so the AI
aims to reply in a compact, high-density style while preserving technical substance.

It only ever adds text to **your outgoing message**: the first message of a
conversation gets the full primer (the caveman skill, compacted), and every
message after gets a tiny "stay caveman" reminder. The injected directive is
plainly visible in the chat. No network requests, no analytics — everything runs
locally in your browser.

The primer also tells the model to use TOON fences for large tabular structured
output when compactness matters and you did not request a specific
format/protocol, while explicitly avoiding TOON for tool-call arguments or code.

## Install (unpacked)

1. Open `chrome://extensions`, turn on **Developer mode**.
2. **Load unpacked** → pick this `extension/` folder.
3. Pin the icon. It ships **on** for all three sites.

Works in any Chromium browser (Chrome, Edge, Brave, Arc). No build step.

## Use

- Turn it on from the popup (and pick an intensity: **lite / full / ultra**).
- Type and send as normal. The extension prepends the caveman directive and
  submits — the AI's replies come back terse.
- The on-page flame pill shows it's active; click it to turn off (or say
  `stop caveman` in chat).

## How it works

- `src/directive.js` — the caveman skill, compacted into a `buildPrimer` /
  `buildReminder` text (pure, unit-tested; no chrome/DOM).
- `src/caveman.js` — content script: intercepts the send gesture (Enter or the
  send button) in the capture phase, prepends the directive via the editor's own
  input path, then re-fires the send. Resilient per-site selectors with
  fallbacks; never touches network requests or model replies.
- `src/background.js` — service worker: reflects the on/off state on the toolbar
  badge.

## Develop

```bash
npm ci                                # installs pinned Playwright tooling
npm run setup:browser                 # installs pinned Chromium once
npm test                              # pure directive + service-worker tests
npm run test:browser                  # Chromium content-script + popup journeys
npm run package                       # all tests + verified deterministic store zip
```
