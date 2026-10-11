# Changelog

## Unreleased

- OMP subagents (OMP 18.5.1 or newer, where the extension context carries
  `ctx.agent`) are no longer treated as Caveman sessions of their own. They report
  under the main session's id, start with `SubagentStart` and end with
  `SubagentStop`, send no `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `Stop`,
  `PreCompact` or `PostCompact`, print no direct-mode notices, and route through a
  live proxy like their parent.
- **Behavior change:** Core is no longer injected into OMP subagents. Only context
  returned by `SubagentStart` reaches them (none today), as in Claude Code.

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
