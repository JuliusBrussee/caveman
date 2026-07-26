# public/shared/contracts — shared wire contracts (JSON Schema)

Source-of-truth schemas for cross-service/SDK wire shapes. One schema:
`schemas/policy.schema.json` — tenant policy object validated at config load time
by `cloud/control-api`, referenced by `cloud/web/lib/types.ts`.

## Layout

- `schemas/policy.schema.json` — JSON Schema 2020-12 for the tenant policy object (required: version, runtime_mode, fail_policy, providers, limits, retention, optimizers, telemetry, sdk)
- `package.json` — build/lint/test scripts all validate JSON parse of the schema (no compiled output)

## Key schema fields

- `runtime_mode` — `record | recommend | shadow | canary | active | compress`; controls optimizer rollout stage
- `fail_policy` — `fail_open | fail_closed`; unknown values fail closed (honesty rule)
- `limits.monthly_usd` — `{ soft, hard }` spend caps per tenant
- `telemetry.sample_rate` — float 0–1, validated by schema minimum/maximum
- `additionalProperties: true` — intentional; services and optimizers may extend the object

## Conventions

- Schema version is an integer; `policy_schema_version` returned by `cloud/control-api/internal/httpapi/server.go:372`
- Adding a new required field is a **breaking change** — bump `version` and coordinate across control-api, SDKs, web
- No TypeScript types generated from schema yet; `cloud/web/lib/types.ts` manually mirrors the shape

## Gotchas

- `fail_policy: fail_closed` aligns with repo honesty rule: unknown enum cases must fail closed, not pass through
- `runtime_mode: record` is always pass-through (byte-safe rule); never treat it as an optimizer mode
- Build script is just a JSON parse check — no compile step or emitted artifact

See ../../../CLAUDE.md (root) · ../../../docs/design.md
