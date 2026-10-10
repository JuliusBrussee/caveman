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
- Removed three rows for retired models (checked 2026-10-10). These are removals,
  so no price was re-checked and no snapshot was added; older snapshots keep
  the rows. The rows:
  - Bedrock `anthropic.claude-3-5-haiku-20241022-v1:0` (us-east-1): AWS end of
    life 2026-06-19, from Amazon Bedrock's model lifecycle page as archived on
    2026-02-20 and 2026-04-09.
  - Bedrock `anthropic.claude-3-5-sonnet-20241022-v2:0` (us-east-1): past AWS
    end of life. The archived lifecycle page gives 2026-03-01 for us-east-1;
    the current regional availability page gives 2026-07-30.
  - Anthropic `claude-opus-4-1`: retired on the Claude API 2026-08-05 (Anthropic
    model deprecations page).

  Lookups for these IDs now return an honest unpriced zero.

## 1.0.0 — 2026-07-26

- Recorded stable provider-catalog schema and honest-zero lookup contract.
- Clarified that price rows are versioned data and may change without a package
  bump; schema breaks require a major.
