# packages/shared/contracts — shared wire contracts (JSON Schema)

Source-of-truth schemas for cross-service/SDK wire shapes:
`schemas/policy.schema.json` covers tenant policy and
`schemas/practice.schema.json` covers practice-library records.

## Layout

- `schemas/policy.schema.json` — JSON Schema 2020-12 for the tenant policy object (required: version, runtime_mode, fail_policy, providers, limits, retention, optimizers, telemetry, sdk)
- `schemas/practice.schema.json` — strict JSON Schema 2020-12 for one practice record; unknown fields and enums fail closed
- `schemas/adapter-conformance.schema.json` — static Pi/Claude contract-record
  shape; `evidence: static_contract_only` explicitly denies executable proof
- `scripts/validate-schemas.mjs` — compiles every schema with AJV, validates
  adapter fixtures, and checks their shared static contract fields
- `package.json` — build/lint/test scripts run schema validation (no compiled output)

## Key schema fields

- `capabilities` — `{ compress?, cache_hints?, routing? }` booleans, `additionalProperties: false`; the authoritative description of what a project optimizes. An unordered SET, not a stage — the three compose
- `pass_through` — boolean; forbids every transform. Outranks `capabilities`, which are retained so operator toggles keep their remembered positions
- `runtime_mode` — `record | recommend | shadow | canary | active | compress`; **a DERIVED display string, not an input**. Still `required` for the compat window (removal 2026-10-01) because `projects.runtime_mode` and the telemetry write validator read it. A doc carrying `capabilities` is authoritative and its `runtime_mode` derives from them; a legacy doc carrying only `runtime_mode` derives the capability set
- `fail_policy` — `fail_open | fail_closed`; unknown values fail closed (honesty rule)
- `limits.monthly_usd` — `{ soft, hard }` spend caps per tenant
- `telemetry.sample_rate` — float 0–1, validated by schema minimum/maximum
- `additionalProperties: true` — intentional; services and optimizers may extend the object

## Conventions

- Schema version is an integer; `policy_schema_version` returned by `cloud/control-api/internal/httpapi/server.go:372`
- Adding a new required field is a **breaking change** — bump `version` and coordinate across control-api, SDKs, web
- No TypeScript types generated from schema yet; the web dashboard manually mirrors the shape

## Gotchas

- `fail_policy: fail_closed` aligns with repo honesty rule: unknown enum cases must fail closed, not pass through
- `runtime_mode: record` is always pass-through (byte-safe rule); never treat it as an optimizer mode
- The six `runtime_mode` values are **not ordered**. Code comparing them for rank (`mode >= "canary"`) is a bug; the four middle values are behaviourally identical, and `shadow`/`canary` are display labels rather than traffic splits. The derivation lives in exactly one place, `cloud/internal/capability`, because the gateway, control-api, and the worker must never disagree about what a document means
- Build script validates schemas and static fixtures; it emits no artifact and
  does not prove executable Pi/Claude parity

See ../../../CLAUDE.md (root)
