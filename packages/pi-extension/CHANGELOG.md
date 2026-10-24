# Changelog

## Unreleased

- Send the per-prompt runtime context as a hidden message of the run, not in
  the system prompt. A system prompt that changed at every prompt made the
  provider re-write its prompt cache from the system prompt onward, twice per
  prompt (#1364). Core context still rides the system prompt, byte-stable.

## 0.3.1 — 2026-10-13

- Read Caveman 4's config home (`$CAVEMAN_HOME/cloud.json`), its `localPort`,
  and skip a login-only gateway URL, so the npm-installed extension finds the
  proxy that `caveman setup` started instead of staying in direct mode.
- Keep the session's thinking level when routing or restoring a model. Pi's
  `setModel` re-derives it from settings, which dropped `--thinking high` and a
  resumed session's level.
- Accept Pi 1.1+ `before_agent_start` events for extension-sent custom
  messages, which carry no prompt.
- Windows: launch npm `.cmd` shims that forward to a native `.exe`, and hide the
  console window each hook child opened.
- Do not install this package alongside `caveman enable pi`: both register
  `caveman_retrieve`, and Pi refuses to start on the conflict. Pick one.

## 0.3.0 — 2026-10-05

- Route Pi's built-in `openai-codex` ChatGPT OAuth traffic through Caveman's
  dedicated subscription proxy when the running proxy advertises the exact
  ChatGPT backend.
- Decode Pi's zstd-compressed Codex SSE request bodies for live-zone compression,
  then re-encode them as zstd; malformed/unknown encodings stay byte-exact
  pass-through and transformed 4xx responses retry the exact original wire bytes.
- Before shortening a tool result, check that its recovery handle resolves to
  the original bytes; unverifiable handles, or companions without verification
  support, leave the output unchanged.
- Track Pi 1.0 (pinned against `@earendil-works/pi-coding-agent` 1.0.4) and
  pi-ai 1.0 compat defaults.
- Windows: support the managed Pi launcher and unwrap nested Pi command shims.

## 0.2.0 — 2026-09-24

- **Breaking (license):** relicensed from MIT to Apache-2.0, along with the rest of
  the repository in Caveman 3.0.0. Releases before this one keep the MIT license.
- The tarball now ships `LICENSE` and `NOTICE`.
