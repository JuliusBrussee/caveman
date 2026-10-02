# Changelog

## Unreleased

- Route Pi's built-in `openai-codex` ChatGPT OAuth traffic through Caveman's
  dedicated subscription proxy when the running proxy advertises the exact
  ChatGPT backend.
- Decode Pi's zstd-compressed Codex SSE request bodies for live-zone compression,
  then re-encode them as zstd; malformed/unknown encodings stay byte-exact
  pass-through and transformed 4xx responses retry the exact original wire bytes.

## 0.2.0 — 2026-09-24

- **Breaking (license):** relicensed from MIT to Apache-2.0, along with the rest of
  the repository in Caveman 3.0.0. Releases before this one keep the MIT license.
- The tarball now ships `LICENSE` and `NOTICE`.
