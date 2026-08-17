# packages/shared/contracts — shared wire contracts (JSON Schema)

Source-of-truth schemas for cross-service/SDK wire shapes:
`schemas/policy.schema.json` covers tenant policy and
`schemas/practice.schema.json` covers practice-library records.

## Layout

- `schemas/policy.schema.json` — JSON Schema 2020-12 for the tenant policy object (required: version, runtime_mode, fail_policy, providers, limits, retention, optimizers, telemetry, sdk)
- `schemas/practice.schema.json` — strict JSON Schema 2020-12 for one practice record; unknown fields and enums fail closed
- `schemas/adapter-conformance.schema.json` — static Pi/Claude/Vercel AI SDK/Eve/
  Mastra contract-record shape; `evidence: static_contract_only` explicitly
  denies executable proof
- `schemas/canonical-span.schema.json` — `caveman.span.v1`, the normalized
  evidence-plane span shared by trace intelligence and replay
- `schemas/agent-run-receipt.schema.json` — `caveman.agent.run-receipt.v1`,
  content-blind per-run SDK economic receipt. It is a shared wire shape, not an
  anonymous CLI event and not a signed verified-savings receipt. Retry events
  carry reserved spend, measured spend, and measurement basis
- `schemas/continuous-improvement-report.schema.json` — versioned end-to-end
  improvement report; the current generated result is E3 `inferred` /
  `symbolic_counterfactual`. E4 `replayed_counterfactual` remains a reserved
  actual recorded-output/sandbox shape and never means verified savings
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
- `runtime_policies` — OPTIONAL array (additive, nothing new required): the per-task-family runtime policies delivered to SDK callers through the policy overlay and served by the gateway's `GET /sdk/v1/runtime-policy`. Guard ops are a closed set (`eq|ne|gt|gte|lt|lte|in`, AND-ed); `budget` is an advisory cap the caller enforces, never a savings figure; the renderer drops a structurally invalid policy WHOLE rather than stripping the broken guard or experiment, because either strip would widen who the policy applies to
- `additionalProperties: true` — intentional; services and optimizers may extend the object

## Conventions

- Schema version is an integer; `policy_schema_version` returned by `cloud/control-api/internal/httpapi/server.go:372`
- Adding a new required field is a **breaking change** — bump `version` and coordinate across control-api, SDKs, web
- No TypeScript types generated from schema yet; the web dashboard manually mirrors the shape
- `scripts/check-contract-compat.mjs` **at the REPO ROOT** (`make check-contract-compat`), not this package's `scripts/`, is a bounded 2020-12 compatibility gate: it proves common narrowing constraints (types, enums/consts, bounds, object/array applicators, and supported combinators/refs) and treats changed unknown assertion/applicator keywords or unresolved reference semantics as major-version review items. Metadata annotations remain non-breaking. It is intentionally not a complete JSON Schema subsumption prover; add a focused fixture before relying on a new keyword.

## Gotchas

- `fail_policy: fail_closed` aligns with repo honesty rule: unknown enum cases must fail closed, not pass through
- `runtime_mode: record` is always pass-through (byte-safe rule); never treat it as an optimizer mode
- The six `runtime_mode` values are **not ordered**. Code comparing them for rank (`mode >= "canary"`) is a bug; the four middle values are behaviourally identical, and `shadow`/`canary` are display labels rather than traffic splits. The derivation lives in exactly one place, `cloud/internal/capability`, because the gateway, control-api, and the worker must never disagree about what a document means
- Build script validates schemas and static fixtures; it emits no artifact and
  does not prove executable cross-harness parity

See ../../../CLAUDE.md (root)
