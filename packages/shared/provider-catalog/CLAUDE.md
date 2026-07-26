# packages/provider-catalog — provider + model pricing catalog

Single source of truth for token prices (input, output, cache-read, cache-write, reasoning,
batch) consumed by the gateway and optimizer for cost accounting and savings math. No runtime
code — data + schema only.

## Layout

- `catalog/current.yaml` — live catalog (26 exact provider/model/region rows across OpenAI, Anthropic, Gemini, Bedrock, Vertex); loaded by services
- `catalog/YYYY-MM-DD.yaml` — dated snapshots kept alongside current; never delete old snapshots
- `schemas/provider-catalog.schema.json` — JSON Schema (draft 2020-12) that every catalog file must satisfy

## Conventions

- Each entry requires: `provider`, `model`, `region`, `currency`, `pricing`, `capabilities`, `sources`, `verified_at`
- `pricing` fields that don't apply to a model must be `null`, not omitted (schema allows `["number","null"]`)
- `verified_at` is an ISO 8601 datetime; update it when you change any pricing/capability/source field; cite real URLs in `sources`
- Adding a new model: add to `current.yaml` AND copy to a new dated snapshot (e.g. `2026-07-01.yaml`)
- `go test ./public/shared/platform/catalog` requires every current row to be byte-semantically identical to the immutable snapshot named by its `verified_at` date; never reuse an old date for a changed row
- `capabilities` keys are free-form booleans; match the provider's actual API surface (e.g. `prompt_cache`, `explicit_cache`, `batch`)

## Gotchas

- **no-fake-savings**: prices here feed the Cave Plan headline — wrong prices → wrong inferred savings. Always verify against the cited provider pricing page before committing a change.
- `cache_write_input_per_million` is `null` for OpenAI and Gemini (they don't charge a separate write fee); Anthropic charges both read and write.
- `batch_discount_fraction: 0.50` means 50 % off, not 50 % of the listed rate — keep that interpretation consistent.
- `build`/`lint`/`test` scripts in `package.json` only parse the schema JSON; they do NOT validate catalog YAML.

See ../../../CLAUDE.md (root) · ../../../docs/design.md
