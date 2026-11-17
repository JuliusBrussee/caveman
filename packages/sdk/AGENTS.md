# packages/sdk

Two client SDKs for the Caveman gateway's `/sdk/v1/*` API and the framework middleware protocol, plus the fixtures that keep them identical:

- `typescript/`: `@caveman-ai/sdk` (npm). Zero runtime dependencies, Node >= 22.12. `src/index.ts` holds the client (`Cave`, `CaveTrace`, tool search, compress, context pack, checkpoints, artifacts, OTLP exporter, runtime policy, retry-loop breaker, provider clients). `src/middleware/` is the `@caveman-ai/sdk/middleware` subpath (protocol 1.1 client, rules, validation, and a lazy `node:http` transport for proxies and custom CAs that never proxies loopback). `scripts/cjs-types.mjs` emits the `.d.cts` types.
- `python/`: `caveman-sdk` on PyPI, imported as `caveman_cloud` (bare `caveman` on PyPI is another project). Stdlib only (`urllib.request`), Python >= 3.11. `caveman_cloud/core.py` holds the client; `caveman_cloud/middleware/` has the sync and async middleware clients and transport.
- `parity/`: language-neutral fixtures both SDKs run. Treat them as a release gate, not documentation.

## Build and test

From the repo root:

```bash
pnpm --filter @caveman-ai/sdk test     # build, tsc type tests, node --test tests/*.runtime.mjs
PYTHONPATH=packages/sdk/python python -m pytest -q packages/sdk/python/tests
```

CI: `engine-ci.yml` runs both; `middleware-python.yml` runs the Python suite on 3.11 to 3.13. TS type assertions live in `tests/*.test.ts` (compiled only); runtime tests in `tests/*.runtime.mjs` import from `dist/`, so build first. Python tests patch `urllib.request.urlopen`; they never touch the network.

Releases: tags `sdk-ts-v*` and `sdk-python-v*` through `release-packages.yml`. `tests/verify_repo.py` checks that the package version, the TS `SDK_VERSION` constant (`src/middleware/types.ts`), the top `CHANGELOG.md` heading and the README install pins agree. Only `dist/`, `README.md`, `LICENSE`, `NOTICE` (npm) and the `caveman_cloud` packages (PyPI) ship.

## Parity contract

| Fixture | Run by |
|---|---|
| `fixtures.json` | TS `parity.runtime.mjs`, Python `test_parity.py` (and transport tests) |
| `runtime-policy.fixtures.json` | TS `runtime-policy.runtime.mjs`, Python `test_runtime_policy.py` |
| `middleware-v1_1.fixtures.json` | both SDKs' middleware tests, `proxy/internal/middleware` Go tests, `packages/shared/contracts` conformance |
| `middleware.fixtures.json`, `middleware-preflight.fixtures.json` | both SDKs' middleware tests; `middleware.fixtures.json` also `packages/middleware/*` |

- `fixtures.json` has one `config`, named header sets (`std_headers`, `std_headers_traced`, ...) and an ordered `operations` list. Each operation has an `input`, a canned `response` (or `transport: "error"` to force pass-through) and an `expect` with the `wire` (method, path, headers, exact `body` or `body_keys`) and the `result`.
- Each SDK has one handler per operation that makes the real SDK call, captures the wire and normalizes the result to snake_case. Both iterate every operation; a missing handler fails, never skips.
- Adding a field or method to one SDK means adding the operation here and making the other SDK pass. Change one SDK, change both and the fixtures.
- Iterate fixture arrays (`assignment_vectors`, `guard_cases`, `decision_cases`); never hard-code their counts. Assignment vectors compare floats exactly.
- Headers compare lowercased (Python capitalizes them). Avoid values that encode differently in the two languages (`! ( ) *` in expand refs).
- Nothing random reaches an assertion: ids the SDK would mint (trace id, span id) are injected through `input` and pinned in the header set.

## Invariants

- No runtime dependencies in either SDK. Do not add `requests`, `httpx` or an npm dependency.
- Byte-safe: request bodies go to the gateway unmodified. `compress()` delegates to the Engine through `/sdk/v1/compress` and, on any transport or parse problem, returns the original input with ratio 0 and no handle. The SDKs never reimplement a compressor or compute tool-search similarity (`ranker` passes through verbatim).
- Every savings or token figure is `basis: "inferred"`, with the counter disclosed (`tokenBasis` / `token_basis`). Runtime-policy decision spans carry no savings, dollar or `verified` field.
- Context packing (`/sdk/v1/context/pack`) is connected-only and lossy: the caller must keep every item named in `deferredIds` / `deferred_ids`. A transport or malformed-report failure returns all original items with zero savings.
- Tracing: a trace mints a 32-hex trace id and a 16-hex root span id; a supplied id that is not that exact shape is replaced. Provider calls and trace-scoped `/sdk/v1/*` calls (events, checkpoints, artifacts) made through the trace carry `x-cave-trace-id` and `x-cave-parent-span-id`; calls made directly off `Cave` carry neither.
- `x-cave-workflow` is always sent (default `unlabeled-workflow`; `CAVE_WORKFLOW` applies when valid). Python builds every header in `headers()`; edit there only.
- Deferred tool search sends the request `session_id`, returns `sessionId` / `session_id` and uses the provider header `x-cave-tool-session`. `strategy: "deferred"` returns `alwaysLoad` tools plus up to `initialToolCount` (default 8) and never the full catalog without a search.
- Runtime policy: `refresh()` is the only network call (`GET /sdk/v1/runtime-policy`, 30 s timeout, 1 MiB cap `oversized_response`). It verifies the Ed25519 signature over the bundle string before parsing, pins the key on first verified signature (before schema and sequence checks, so a signed but rejected bundle cannot open a downgrade window), rejects a lower `sequence` and keeps the last good bundle on any failure. TS fails closed (`bounded_response_unavailable`) when fetch gives no bounded body. `decide()` is synchronous, local and never throws; it never guesses an arm for a missing unit key.
- `jobs` is a reserved surface: every method fails locally with `cave_async_jobs_unavailable` and makes no request.

See the root `AGENTS.md` for repo-wide rules.
