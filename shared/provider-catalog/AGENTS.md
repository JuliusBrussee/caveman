# shared/provider-catalog

Dated provider and model prices (input, output, cache read/write, reasoning, batch, long context) plus model capabilities. The proxy prices spend rows, routing and `caveman learn` from it through `shared/platform/catalog`, which embeds `catalog/current.yaml` via `embed.go`. This folder holds data and validators only.

## Layout

- `catalog/current.yaml`: the live catalog, one row per provider/model/region.
- `catalog/YYYY-MM-DD.yaml`: immutable dated snapshots. Never edit one, and never delete a snapshot that a current row's `verified_at` names.
- `schemas/provider-catalog.schema.json`: JSON Schema 2020-12 for every catalog file.
- `validate_catalog.py`, `tests/test_validate_catalog.py`: the Python validator and its failure-mode tests.
- `catalog_test.go`: Go checks on the current rows (routing fields, Bedrock and Mistral regional prices, null-capability notes).
- `CONTRIBUTING.md`: how to submit a price change.

## Test

From the repo root:

```bash
go test ./shared/provider-catalog ./shared/platform/catalog
python -m pip install -r shared/provider-catalog/requirements-dev.txt
python shared/provider-catalog/validate_catalog.py
python -m unittest discover -s shared/provider-catalog/tests -p 'test_*.py'
```

`provider-catalog.yml` runs the Python checks on any PR touching `catalog/`. That lane requires the PR to change `current.yaml`, add one new dated snapshot, and touch nothing else, so price review stays separate from code review.

## Invariants

- Every row needs `provider`, `model`, `region`, `currency`, `pricing`, `capabilities`, `sources`, `verified_at`. Pricing fields that do not apply are `null`, not omitted.
- `verified_at` means one thing: the date this row's price was last checked against the vendor's pricing page. It alone feeds `catalogVersion()`, which goes into cost reports and signed receipts. Bump it only when you re-check the price, and never reuse an old date for a changed price. It may not be in the future or older than 120 days.
- Every current row's price identity (`provider`, `model`, `region`, `currency`, `pricing`, `verified_at` and the price-affecting capabilities) must equal the snapshot named by its `verified_at` date. A price change therefore adds a new dated snapshot.
- `capabilities_verified_at` dates the capability data a row asserts against the vendor's model docs. A capability-only edit bumps it and leaves `verified_at` alone. Replacing a guess with `null` withdraws a claim, which is not a check, so it does not bump the date.
- Some `capabilities` keys are price: `catalog.PriceAffectingCapabilities` (`regional_processing_multiplier`, `inference_geo_us_multiplier`, `region_agnostic_pricing`). Editing one is a price edit. A new price-affecting key must be registered there or `PricingMultiplier` ignores it; the Python list in `validate_catalog.py` must match (a test parses the Go file).
- Unknown is not `false`. `tools`, `vision` and `json_mode` are `null` when the vendor's docs do not say, with a YAML comment naming the unverified keys (`TestNullCapabilitiesCarryAnUnverifiedNote`). A guessed `true` routes traffic to a model that may lack the feature; a guessed `false` removes a candidate.
- An unknown provider, model or region is priced at zero with `unpriced:` provenance. Never borrow a sibling model's price.
- A row carrying a `# proposed-by:` marker fails validation until a reviewer checks the price against the provider's page and removes the marker.
- `batch_discount_fraction: 0.50` means 50% off the listed rate.
- Wrong prices produce wrong spend and wrong inferred savings. Cite provider-owned HTTPS pages in `sources`.

See the root `AGENTS.md` for repo-wide rules.
