# Changelog

## 2.0.0 — 2026-09-24

- **Breaking (license):** relicensed from MIT to Apache-2.0, along with the rest of
  the repository in Caveman 3.0.0. Releases before this one keep the MIT license.
- **Breaking (package):** renamed `@caveman/contracts` → `@caveman-ai/contracts`.
  `package.json` had stayed at 1.0.0 after the 1.1.0 entry below; this release
  realigns them.
- **Breaking (schema IDs):** every `$id` is now
  `https://raw.githubusercontent.com/JuliusBrussee/caveman/contracts-v2.0.0/packages/shared/contracts/schemas/<file>`,
  pinned to this release's immutable tag (previously `https://caveman.so/schemas/…`
  and `https://caveman.cloud/schemas/…`, which did not resolve). Every
  cross-schema `$ref` is relative (`middleware-common.schema.json#/$defs/token`),
  so tools that load the schemas or the OpenAPI document from the package
  directory resolve them locally, with no network access. Each release moves
  the tag in every `$id`; `scripts/validate-schemas.mjs` fails when an `$id`
  does not embed the `package.json` version or a `$ref` is absolute. No wire
  shape changed because of this.
- Middleware protocol 1.1 (additive; `schema_version` stays 1), specified in
  `docs/technical/middleware-protocol.md`:
  - `middleware-capabilities`: optional `protocol {min,max}`, `features`,
    `max_retention_seconds`; optional `limits` keys `retrieve_deadline_ms`,
    `queue_depth`, `retrieve_queue_depth`, `max_segments`,
    `max_manifest_items`, `receipt_bytes`, `quota_requests_per_minute`.
    Any other `limits` key must be a positive safe integer (protocol 1.0
    clients reject anything else).
  - New: `middleware-error`, `middleware-session-delete`,
    `middleware-session-delete-response`, `middleware-receipt-response`,
    `middleware-decision-event`; `middleware-common` gains `feature`,
    `reason`, `positive_limit`.
  - New `openapi/middleware.openapi.json` (OpenAPI 3.1) covering
    `capabilities`, `optimize`, `retrieve`, `receipts`, `sessions/delete`.
  - The response schemas are closed (`additionalProperties: false`) on
    purpose: they describe exactly what a 1.1 server emits and are what the
    conformance kit checks servers against. Clients must not validate
    responses against them (spec §4, §16); each schema's `description` says so.
- `build` / `lint` / `test` run both `scripts/validate-schemas.mjs` and
  `scripts/validate.mjs`. The former now also checks `$id` against the file
  name, the protocol 1.1 fixture examples, and every OpenAPI `$ref`.

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
