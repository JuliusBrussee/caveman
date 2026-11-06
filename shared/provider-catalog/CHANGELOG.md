# Changelog

## Unreleased

- Re-checked the prices of 34 rows against the vendors' pricing pages on
  2026-11-06 (snapshot `2026-11-06.yaml`): every OpenAI, Anthropic, Gemini API
  and Bedrock row last checked 2026-07-10 or 2026-07-23, plus Vertex
  `claude-sonnet-4-6` and `claude-haiku-4-5@20251001`. No price changed. Added
  the pages the check used as sources where they were missing: OpenAI's
  developer pricing page (and Batch guide for the two embedding rows) and
  Google Cloud's partner-model pricing page for the two Vertex Claude rows.
- Removed two rows for retired models: Vertex `gemini-2.5-pro` and
  `gemini-2.5-flash` (global). Google's Vertex model-versions page and both
  model pages give a retirement date of 2026-10-20, with `gemini-3.8-flash` /
  `gemini-3.5-flash(-lite)` as replacements. Their prices were not re-checked,
  so they are absent from `2026-11-06.yaml`; older snapshots keep the rows.
  Vertex Gemini lookups now return an honest unpriced zero, and the catalog has
  no Vertex Gemini rows until a current model is added with a checked price.
  These were the only rows carrying `region_agnostic_pricing`. The capability
  stays registered and pinned, and its tests now run on a fixture catalog.
  The Gemini API rows for the same model ids are unaffected.
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
