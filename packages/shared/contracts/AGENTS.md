# packages/shared/contracts

`@caveman-ai/contracts` (npm): the JSON Schema 2020-12 and OpenAPI wire contracts shared by the proxy, the SDKs, the middleware packages and the hosted service. No compiled output; the package ships `schemas/`, `openapi/`, `CHANGELOG.md`, `LICENSE` and `NOTICE`.

## Layout

- `schemas/policy.schema.json`: the tenant policy object.
- `schemas/practice.schema.json`: one practice-library record; strict, unknown fields and enums fail closed.
- `schemas/middleware-*.schema.json` and `openapi/middleware.openapi.json`: the framework middleware protocol. Prose rules live in `docs/technical/middleware-protocol.md`; executable vectors in `packages/sdk/parity/middleware-v1_1.fixtures.json`.
- `schemas/route-ask-v1.schema.json`, `runtime-event-v1.schema.json`: what the proxy's route stage sends to Caveman Cloud.
- `schemas/canonical-span.schema.json` (`caveman.span.v1`), `agent-run-receipt.schema.json` (content-blind per-run receipt, not a verified-savings receipt), `continuous-improvement-report.schema.json`, `adapter-conformance.schema.json` (`evidence: static_contract_only` denies executable proof) and the rest of `schemas/`.
- `scripts/validate-schemas.mjs`: compiles every schema with AJV, validates fixtures and middleware examples, checks `$id`s, relative cross-schema `$ref`s and every OpenAPI `$ref`.
- `scripts/validate.mjs`: every `X.json` with a sibling `X.schema.json` must satisfy it.
- `conformance/run.mjs`: black-box HTTP conformance kit for any middleware runtime (see `conformance/README.md`).

## Build and test

```bash
bun run --filter @caveman-ai/contracts test    # both validators; build and lint run the same
```

CI: `middleware-conformance.yml`. Releases: `contracts-v*` tags through `release-packages.yml`.

## Invariants

- Every schema `$id` is the raw GitHub URL of the file at tag `contracts-v<package.json version>`. Bump the version and the `$id`s together.
- Adding a required field is a breaking change: bump the schema `version` and update the proxy, both SDKs and the middleware packages together.
- Middleware changes stay additive under `schema_version` 1. Unknown `limits` keys must stay positive safe integers, because SDK 1.1.0 rejects anything else.
- Policy `capabilities` (`compress`, `cache_hints`, `routing`) is an unordered set and the authority on what a project optimizes. `pass_through: true` forbids every transform and outranks it.
- `runtime_mode` (`record | recommend | shadow | canary | active | compress`) is a derived display string, still `required` for older readers. The values are not ordered: comparing them by rank is a bug, and `shadow`/`canary` are labels, not traffic splits. `record` is always pass-through.
- `fail_policy` is `fail_open | fail_closed`; unknown values fail closed.
- `runtime_policies` guard ops are a closed set (`eq|ne|gt|gte|lt|lte|in`, AND-ed). `budget` is an advisory cap, never a savings figure. A structurally invalid policy is dropped whole; stripping only the broken guard or experiment would widen who it applies to.
- The policy object keeps `additionalProperties: true` on purpose so services can extend it.
- The validators check schemas and static fixtures only; they do not prove cross-harness behavior.

See the root `AGENTS.md` for repo-wide rules.
