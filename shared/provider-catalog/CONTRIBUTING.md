# Contributing provider prices

Price updates are public data changes, but every number must remain traceable.
One pull request updates `catalog/current.yaml` and adds an immutable dated
snapshot.

Rules enforced by the public validator and the monorepo's deeper Go tests:

1. `verified_at` is RFC3339, not future-dated, and no older than 120 days.
2. Every current row must match byte-for-byte by decoded value with
   `catalog/<YYYY-MM-DD>.yaml`, where date comes from that row's `verified_at`.
   A price change therefore adds a new dated file; never rewrite an old
   snapshot.
3. `sources` contains HTTPS links to provider-owned pricing or model
   documentation supporting changed values. Name source and access date in pull
   request.
4. Unknown provider, model, or region combinations stay zero-priced with
   `unpriced:` provenance. Never borrow a nearby model or guess.

Run:

From this package directory:

```bash
python3 -m pip install -r requirements-dev.txt
python3 validate_catalog.py
python3 -m unittest discover -s tests -p 'test_*.py'
```

Catalog lane runs only these focused tests for catalog-only pull requests.
Mixed product and catalog changes must be split so price review stays auditable.
