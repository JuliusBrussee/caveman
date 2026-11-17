# Contributing provider prices

Price updates are public data changes, and every number must stay traceable.
One pull request updates `catalog/current.yaml` and adds one new dated snapshot
(`catalog/YYYY-MM-DD.yaml`). It touches nothing else: the catalog CI lane
rejects mixed changes so price review stays auditable.

Rules enforced by `validate_catalog.py` and the Go tests in
`shared/platform/catalog`:

1. `verified_at` is RFC3339, not in the future, and no older than 120 days.
2. Every current row must match, by decoded value, its row in
   `catalog/<YYYY-MM-DD>.yaml`, where the date is that row's `verified_at`. A
   price change therefore adds a new dated file. Never rewrite an old snapshot.
3. `sources` holds HTTPS links to provider-owned pricing or model documentation
   that support the changed values. Name the source and access date in the pull
   request.
4. Unknown provider, model or region combinations stay unpriced (zero, with
   `unpriced:` provenance). Never borrow a nearby model's price or guess.

Run from the repository root:

```bash
python3 -m pip install -r shared/provider-catalog/requirements-dev.txt
python3 shared/provider-catalog/validate_catalog.py
python3 -m unittest discover -s shared/provider-catalog/tests -p 'test_*.py'
go test ./shared/provider-catalog ./shared/platform/catalog
```

See `AGENTS.md` in this folder for what `verified_at`, `capabilities_verified_at`
and the price-affecting capability keys mean.
