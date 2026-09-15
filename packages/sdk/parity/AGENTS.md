# packages/sdk/parity — the cross-language SDK conformance contract

One language-neutral fixture file, run by **both** SDKs. The load-bearing honesty property
of the SDKs: the wire contract is one thing, expressed twice (`@caveman-ai/sdk` TS +
`caveman_cloud` Python). A field present in one SDK and absent in the other makes one side's
assertion fail — so this folder is a release gate, not documentation.

## Layout

- `fixtures.json` — the contract. A `config` (one Cave), the named header sets (`std_headers` / `std_headers_traced` / `std_headers_async_traced` / `otlp_headers`), and an ordered `operations` list. Each operation carries its `input`, a canned `response` (or `transport: "error"` to force a byte-safe pass-through), and an `expect` block: `wire` (`method`, `path`, `headers`, and either an exact `body` or a `body_keys` set) plus a `result` (or `result_from: "response"`).

## How it's enforced

- TS half: `../typescript/tests/parity.runtime.mjs` (mocks `fetch`).
- Python half: `../python/tests/test_parity.py` (mocks `urllib.request.urlopen`).
- Each half has a per-operation handler that performs the **real** SDK call, captures the wire, and normalizes the result to canonical snake-keyed values. Both iterate **every** operation; a missing handler is a failure, never a skip.

## Editing rules

- Add a field/method to one SDK → add the operation (or body key) here → the other SDK's half goes red until it matches. That red is the point.
- Headers are compared key-lowercased (Python's `urllib` capitalizes them); the SET + values must match across languages.
- Keep values that encode identically in both languages (e.g. avoid `! ( ) *` in expand refs — JS `encodeURIComponent` and Python `quote` disagree on those).
- Nothing random may reach an assertion. Ids the SDK would otherwise mint (trace id, span id) are injected through the operation `input` and pinned in the expected header set — the same way `otlp_export` pins its span ids.
- Run: `make product-test PRODUCT=sdk-ts` && `make product-test PRODUCT=sdk-python`.

See ../typescript/CLAUDE.md · ../python/CLAUDE.md
