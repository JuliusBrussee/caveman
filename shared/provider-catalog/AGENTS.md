# shared/provider-catalog — provider + model pricing catalog

Single source of truth for token prices (input, output, cache-read, cache-write, reasoning,
batch) consumed by the gateway and optimizer for cost accounting and savings math. No runtime
code — data + schema only.

## Layout

- `catalog/current.yaml` — live catalog (44 exact provider/model/region rows across OpenAI, Anthropic, Gemini, Bedrock, Vertex); loaded by services
- `catalog/YYYY-MM-DD.yaml` — dated snapshots kept alongside current; **never delete a snapshot any current row's `verified_at` names.** One narrow exception, already exercised: a snapshot minted by a mistaken *re-attest* — no row references it once the dates are reverted, and it never shipped — is removed in the same change that reverts those dates, because keeping it asserts a price re-check nobody performed (`2026-07-30.yaml`, removed 2026-08-01)
- `schemas/provider-catalog.schema.json` — JSON Schema (draft 2020-12) that every catalog file must satisfy

## Conventions

- Each entry requires: `provider`, `model`, `region`, `currency`, `pricing`, `capabilities`, `sources`, `verified_at`. `capabilities_verified_at` is optional (older snapshots predate it) but every current row should carry one.
- `pricing` fields that don't apply to a model must be `null`, not omitted (schema allows `["number","null"]`)
- **`verified_at` and `capabilities_verified_at` are two different kinds of provenance and must never be conflated:**
  - `verified_at` means exactly one thing — the date this row's **pricing** was last checked against the vendor's published pricing page. It is the sole input to `catalogVersion()` (`public/shared/platform/catalog/catalog.go`), which is embedded in cost reports and **signed receipts**. Bump it only when you re-check prices; never as a side effect of a capability or source edit.
  - `capabilities_verified_at` means the date the capability data this row ASSERTS was last checked against the vendor's own model docs. It is not read by any cost/receipt path — it is embedded in nothing money-related.
    - "Asserts" is load-bearing and narrower than it looks: a key whose value is explicit `null` asserts nothing, so it is **outside** what this date covers. That is what keeps the date honest on the 20 Bedrock rows whose `tools`/`vision`/`json_mode` were never checked — the date still truthfully covers `context_window_tokens` and the other sourced keys those rows do assert. Any row carrying a `null` routing capability MUST also carry a comment naming the unverified keys; `TestNullCapabilitiesCarryAnUnverifiedNote` enforces that pairing, so the narrowing cannot become a loophole.
  - A capability-only edit (adding/correcting `tools`/`vision`/`json_mode`/`context_window_tokens`/capability `sources`) bumps `capabilities_verified_at` and leaves `verified_at` untouched. A price-only edit does the reverse. Only bump both when you genuinely re-checked both against their respective vendor pages the same day.
  - **WITHDRAWING a claim is not a verification and does not bump the date.** Replacing an unverified value with `null` removes an assertion rather than checking one, so `capabilities_verified_at` stays where it was — bumping it would be the same date-attests-work-nobody-did mistake the `verified_at` rule above exists to prevent, one field over.
- Adding a new model: add to `current.yaml` AND copy to a new dated snapshot (e.g. `2026-07-01.yaml`)
- `go test ./public/shared/platform/catalog` requires every current row's **pricing-relevant fields** (`provider`, `model`, `region`, `currency`, `pricing`, `verified_at`, **plus every key in `catalog.PriceAffectingCapabilities`**) to be byte-semantically identical to the immutable snapshot named by its `verified_at` date; never reuse an old `verified_at` date for a row whose price actually changed. The *rest* of `capabilities`, plus `capabilities_verified_at` and `sources`, are deliberately excluded from that comparison — they're free to differ from the archived snapshot, which is exactly what lets a capability-only edit skip minting a new price-dated snapshot.
- **Some `capabilities` keys are price, not capability.** `catalog.PriceAffectingCapabilities` (`public/shared/platform/catalog/catalog.go`) is the authoritative list — today `regional_processing_multiplier`, `inference_geo_us_multiplier` (both multiply the row's token rates via `PricingMultiplier` → `scaleStandaloneTokenRates`) and `region_agnostic_pricing` (decides whether a global row's price answers a regional lookup at all, `PriceForRegionOrAgnostic`). Editing any of them **is** a price edit: bump `verified_at` and mint a dated snapshot exactly as if you had edited `pricing`. `TestTamperedPriceCapabilityBreaksTheSnapshotPin` reproduces what their old exclusion cost — a 1.10 → 1.95 edit inflated every OpenAI us/eu rate by 77%, and deleting one `region_agnostic_pricing` line drops a whole Vertex region's spend to zero, both passing every gate including the snapshot pin a signed receipt's `catalog_version` stands on.
- **Adding a price-affecting capability: register it, or it does nothing.** `PricingMultiplier` returns `(0, false)` for any key absent from `PriceAffectingCapabilities`, and the Python mirror is asserted equal to the Go slice by the test suite. That is deliberate: registration is what pins the key, so an unregistered multiplier cannot silently move money past the snapshot.
- Other `capabilities` keys are free-form (mostly booleans, some numeric); match the provider's actual API surface (e.g. `prompt_cache`, `explicit_cache`, `batch`)
- **Unknown is not `false`.** `tools`/`vision`/`json_mode` are explicitly nullable. Write `null` when the vendor's own model docs do not state the answer, with a YAML comment naming what you checked. The router (`candidateSupports` → `boolCap`) treats `null` exactly like absent, so an honest unknown only costs a routing candidate — while a guessed `true` routes traffic to a model that may not support the feature, and a guessed `false` silently deletes a candidate. 20 Bedrock rows carry `null` today for exactly this reason.

## Gotchas

- **no-fake-savings**: prices here feed the Cave Plan headline — wrong prices → wrong inferred savings. Always verify against the cited provider pricing page before committing a change.
- `cache_write_input_per_million` is `null` for OpenAI and Gemini (they don't charge a separate write fee); Anthropic charges both read and write.
- `batch_discount_fraction: 0.50` means 50 % off, not 50 % of the listed rate — keep that interpretation consistent.
- `build`/`lint`/`test` scripts in `package.json` only parse the schema JSON; they do NOT validate catalog YAML.

See ../../../CLAUDE.md (root)
