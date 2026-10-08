# Changelog

## Unreleased

- Added `claude-sonnet-5-5` ($2 in / $10 out, cache read $0.10 = 0.05x) and
  `claude-haiku-5-5` ($0.10 / $0.50, 5x on every rate for prompts over 100,000
  tokens), checked against Anthropic's pricing page 2026-10-08 (snapshot
  `2026-10-08.yaml`). Mythos 5.1 / Mythos 5 (limited availability) are not added.
- Added `gpt-6.1-sol` ($2 in / $10 out, cached $0.10, cache write $2.50) and
  `gpt-6-luna` ($0.10 / $0.50, cached $0.01, cache write $0.125), both 2x input
  and cache / 1.5x output over 272K input tokens, from OpenAI's pricing and
  model pages (checked 2026-10-08, same snapshot).
- **Breaking (license):** relicensed from MIT to Apache-2.0, along with the rest of
  the repository in Caveman 3.0.0. Releases before this one keep the MIT license.

## 1.0.0 — 2026-07-26

- Recorded stable provider-catalog schema and honest-zero lookup contract.
- Clarified that price rows are versioned data and may change without a package
  bump; schema breaks require a major.
