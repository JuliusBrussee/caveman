# Changelog

## 1.1.0 — 2026-07-26

- Added `schemas/grader-registry.json`: one entry per eval grader type (29,
  including the two legacy Python-only graders) with category, one-sentence
  description, an options JSON Schema, `scored` / `deterministic` /
  `judge_calls`, and the pinned judge prompt templates.
- Added `schemas/grader-registry.schema.json` describing that file.
- `build` / `lint` / `test` now run `scripts/validate.mjs`, which parses every
  file in `schemas/` and validates each data file against its sibling schema
  (previously only `policy.schema.json` was parse-checked).
- Additive only; no existing schema changed.

## 1.0.0 — 2026-07-26

- Recorded stable JSON Schema wire-contract baseline.
- Added major-version gate for removed properties, new required fields, narrowed
  enums, removed schemas, and changed schema IDs.
